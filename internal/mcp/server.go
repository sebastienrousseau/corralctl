// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sebastienrousseau/corralctl/internal/symbols"
)

// ServerName is the public identifier the MCP server advertises to clients.
// Kept stable across versions so clients can match by name rather than
// version. Matches the registry entry slug.
const ServerName = "corral"

// ServerOptions configures a Server. All zero values are valid except Root,
// which must be a non-empty absolute path the server will sandbox itself to.
type ServerOptions struct {
	// Root is the absolute path the server treats as the workspace.
	// All tools and resources reject paths outside this root.
	Root string
	// Version is injected at build time (see cmd.Version); surfaced to
	// MCP clients in the server-info handshake.
	Version string
	// EnableMutations, when true, registers the non-destructive write
	// tools (corral_sync_repo, corral_clone_repo, corral_adopt_repo and
	// the three worktree tools). The read-only tool set is
	// always registered; this flag only unlocks the ones that touch
	// the filesystem or the network.
	EnableMutations bool
	// EnableDestructiveMutations gates corral_delete_repo specifically.
	// A misfiring agent that could delete workspace repos is a class of
	// harm distinct from clone/sync mistakes, so it earns its own opt-in.
	// Ignored unless EnableMutations is also true.
	EnableDestructiveMutations bool
	// IndexCacheDir is where the trigram index files are persisted between
	// processes. Empty means the platform default; "off" disables the index,
	// which makes every content search read every candidate file as it did
	// before the index existed.
	IndexCacheDir string
	// SymbolCacheDir is where extracted symbols are persisted between
	// processes. Empty means the platform default; "off" disables the
	// cache, which is for a machine where the extra directory is
	// unwelcome rather than for correctness — a stale entry cannot be
	// served, because a hit still walks the repository.
	SymbolCacheDir string

	// AllowFileExts extends the file-resource extension allowlist with
	// additional extensions (with or without a leading dot). The default
	// allowlist covers source, documentation and non-secret configuration;
	// this is the escape hatch for a workspace with house extensions.
	// It cannot re-enable a denylisted credential file.
	AllowFileExts []string
	// ConfirmDeletes asks the connected client to confirm each deletion
	// through MCP elicitation, and refuses when no one can be asked.
	//
	// Defaults on. --enable-destructive-mutations is a decision made once
	// at launch; after it, every call was authorised purely by having been
	// registered. The refusal cascade stops mistakes, not a persuaded
	// agent that picks the one clone passing every check.
	ConfirmDeletes bool
	// AuditLogPath is where the JSONL audit log for every mutation is
	// appended. Empty means use the XDG default
	// ($XDG_STATE_HOME/corral/mutations.log). Only consulted when at
	// least one mutation gate is enabled.
	AuditLogPath string
	// AuthToken, when non-empty, requires requests against the HTTP and SSE
	// transports to present a matching Bearer token in the Authorization
	// header. When empty, no bearer authentication is enforced.
	AuthToken string
	// AllowedOrigins lists origins permitted to connect to HTTP and SSE endpoints.
	// When empty, cross-origin browser requests are rejected to protect against
	// DNS rebinding and cross-site request forgery.
	AllowedOrigins []string
	// WatchWorkspace, when true, starts a background filesystem watcher across
	// the workspace root and structural directories to invalidate the workspace
	// scan cache dynamically in real time upon filesystem changes.
	WatchWorkspace bool
	// MetricsAddr, when non-empty, runs a dedicated HTTP listener serving Prometheus
	// telemetry on addr (e.g. 127.0.0.1:9090) even when the MCP server is operating
	// over stdio.
	MetricsAddr string
	// TraceLogPath is where structured JSONL interaction events are appended.
	// When empty, tracing is disabled.
	TraceLogPath string
}

// Server wraps an mcp-go MCPServer with the corral-specific configuration.
// Exposed as a struct (rather than handing back the bare *server.MCPServer)
// so future phases can attach per-server state (search backends, audit
// logger, etc.) without breaking the cmd-layer call site.
type Server struct {
	mcp     *mcp.Server
	opts    ServerOptions
	auditor *Auditor
	tracer  *EventTracer

	// toolNames records every tool registered on this server, so its own
	// surface is knowable in code rather than only in prose.
	toolNames []string
	// symbolDisk persists extraction between processes. Nil when a cache
	// directory could not be created, which degrades to the behaviour
	// before it existed.
	symbolDisk symbols.Cache
	// extraFileExts is opts.AllowFileExts in the normalised lookup form the
	// file resource checks against. Computed once at construction so the
	// hot path does no string work.
	extraFileExts map[string]struct{}

	// indexCache holds per-repository trigram indexes, which decide which
	// files a content search has to open. The indexes themselves live in
	// mapped files under indexDir.
	indexCache *indexCache
	// indexDir is where those files are written. Empty means the platform
	// default; "off" disables persistence and the index with it.
	indexDir string

	// symbolCache holds per-repository symbol extractions. Parsing is far
	// more expensive than the workspace scan, and source changes far less
	// often than the set of repositories does, so it gets its own cache
	// with its own TTL.
	symbolCache *symbolCache

	// confirmDeletes and confirmer implement per-call authority for
	// deletion; repoLocks serialises mutations per clone, which only
	// started to matter once the server could accept concurrent sessions.
	confirmDeletes bool
	confirmer      confirmer
	repoLocks      *repoLocks

	// lastSymbolQuery is when a client last asked something that uses the
	// symbol cache, as UnixNano. Read by the background warmer to decide
	// whether anyone still cares. Atomic because the warmer runs
	// concurrently with request handling.
	lastSymbolQuery atomic.Int64
	// warming is set while a warm pass runs; see warmSymbols.
	warming atomic.Bool

	// scanMu guards the in-memory workspace-index cache below.
	// Every tool and resource handler goes through Server.scan(),
	// which walks the filesystem at most once every scanTTL and
	// returns the cached snapshot in between. This trades a small
	// amount of staleness (see scanTTL) for O(1) amortised cost on
	// bursty client sessions where an agent fires 5-10 tool calls
	// in quick succession.
	scanMu      sync.Mutex
	scanIndex   *Index
	scanExpires time.Time

	metrics *Metrics
}

// scanTTL is how long a workspace scan is considered fresh. 5s is short
// enough that an agent noticing a just-cloned repo can always retry and
// see it, and long enough to amortise a burst of tool calls from a
// single session. The value is deliberately in-package rather than a
// ServerOptions field: this is v0 policy, not a user knob, and giving
// users control over it invites confusion about staleness bugs.
const scanTTL = 5 * time.Second

var scanWorkspace = Scan

// scan returns a cached workspace Index, walking the filesystem only
// when the previous snapshot has expired. Safe for concurrent callers
// (single mutex; the walk itself is not parallel so there is no gain
// from a RWMutex here). On error the cache is not populated and the
// error is propagated so the caller can surface it to the agent.
func (s *Server) scan() (*Index, error) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scanIndex != nil && time.Now().Before(s.scanExpires) {
		return s.scanIndex, nil
	}
	idx, err := scanWorkspace(s.opts.Root)
	if err != nil {
		return nil, err
	}
	s.scanIndex = idx
	s.scanExpires = time.Now().Add(scanTTL)
	return idx, nil
}

// invalidateScanCache drops the cached workspace index so the next
// call to scan() re-walks the filesystem. Used by tests to make
// consecutive assertions against different tree states deterministic
// without waiting scanTTL between them.
func (s *Server) invalidateScanCache() {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.scanIndex = nil
	s.scanExpires = time.Time{}
	if s.metrics != nil {
		s.metrics.incCacheInvalidation()
	}
}

// NewServer constructs and configures a Corral MCP server. It registers
// every read-only tool and resource defined in this package. Returns an
// error when ServerOptions are invalid (notably an unset or non-absolute
// Root) so the cmd layer fails fast instead of starting a server that
// would reject every call.
func NewServer(opts ServerOptions) (*Server, error) {
	if opts.Root == "" {
		return nil, fmt.Errorf("Root must not be empty")
	}
	if !isAbsolutePath(opts.Root) {
		return nil, fmt.Errorf("Root %q must be an absolute path", opts.Root)
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}

	mcpSrv := mcp.NewServer(
		&mcp.Implementation{
			Name:    ServerName,
			Title:   "Corral",
			Version: opts.Version,
		},
		&mcp.ServerOptions{
			Instructions: serverInstructions(opts),
			// A non-nil, empty value replaces the SDK's default, which
			// advertises "logging". The server never emits
			// notifications/message, and Logging is Deprecated in protocol
			// 2026-07-28 (SEP-2577). Tools, resources and prompts are still
			// inferred from what is registered.
			Capabilities: &mcp.ServerCapabilities{},
		},
	)

	s := &Server{
		mcp:            mcpSrv,
		opts:           opts,
		extraFileExts:  normalizeExtraExts(opts.AllowFileExts),
		symbolCache:    newSymbolCache(),
		indexCache:     newIndexCache(),
		indexDir:       indexCacheDir(opts.IndexCacheDir),
		symbolDisk:     newSymbolDiskCache(opts.SymbolCacheDir),
		confirmDeletes: opts.ConfirmDeletes,
		confirmer:      elicitConfirmer{},
		repoLocks:      newRepoLocks(),
		metrics:        newMetrics(),
	}
	if opts.EnableMutations || opts.EnableDestructiveMutations {
		s.auditor = NewAuditor(opts.AuditLogPath)
	}
	if opts.TraceLogPath != "" {
		s.tracer = NewEventTracer(opts.TraceLogPath)
	}
	s.registerTools()
	s.registerSymbolTools()
	s.registerSearchTool()

	s.mcp.AddReceivingMiddleware(cacheHintMiddleware())
	s.mcp.AddReceivingMiddleware(s.toolInstrumentationMiddleware())
	s.registerResources()
	s.registerPrompts()
	if s.opts.EnableMutations {
		s.registerMutationTools()
	}
	if s.opts.EnableDestructiveMutations && s.opts.EnableMutations {
		s.registerDestructiveTools()
	}
	s.mcp.AddReceivingMiddleware(s.unknownToolMiddleware())
	// Added last so it wraps every middleware above: instrumentation has
	// to see the trace a request names.
	s.mcp.AddReceivingMiddleware(metaTraceMiddleware())
	return s, nil
}

// unknownToolMiddleware answers a call to a tool this server does not have
// with a tool result that says so, rather than the SDK's protocol error.
func (s *Server) unknownToolMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			call, ok := req.(*mcp.CallToolRequest)
			if !ok || method != "tools/call" || call.Params.Name == "" || s.hasTool(call.Params.Name) {
				return next(ctx, method, req)
			}
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{
					Text: fmt.Sprintf("unknown tool %q: tools/list names the tools this server offers", call.Params.Name),
				}},
			}, nil
		}
	}
}

// hasTool reports whether a tool of that name was registered on this server.
func (s *Server) hasTool(name string) bool {
	for _, n := range s.toolNames {
		if n == name {
			return true
		}
	}
	return false
}

// AuditLogPath returns the audit log path when mutations are enabled;
// empty otherwise. Exposed for the cmd-layer startup banner.
func (s *Server) AuditLogPath() string {
	if s.auditor == nil {
		return ""
	}
	return s.auditor.Path()
}

// TraceLogPath returns the trace log path when tracing is enabled; empty otherwise.
func (s *Server) TraceLogPath() string {
	if s.tracer == nil {
		return ""
	}
	return s.tracer.Path()
}

// Tracer returns the server's EventTracer, or nil if event tracing is disabled.
func (s *Server) Tracer() *EventTracer {
	return s.tracer
}

// Root returns the sandbox root the server was configured with. Useful
// for the cmd layer's startup-log line and for tests.
func (s *Server) Root() string {
	return s.opts.Root
}

// MutationsEnabled reports whether write-tools are unlocked. Read by the
// tool registry at construction time; surfaced via this accessor so
// future phases can also gate behaviour outside the registry path.
func (s *Server) MutationsEnabled() bool {
	return s.opts.EnableMutations
}
