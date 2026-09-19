// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/sebastienrousseau/corralctl/internal/mcp"
	"github.com/sebastienrousseau/corralctl/internal/version"
	"github.com/spf13/cobra"
)

var (
	mcpRoot                       string
	mcpEnableMutations            bool
	mcpEnableDestructiveMutations bool
	mcpSymbolCache                string
	mcpAuditLog                   string
	mcpAllowFileExts              string
	mcpHTTP                       string
	mcpTransport                  string
	mcpHost                       string
	mcpPort                       int
	mcpAllowRemote                bool
	mcpNoConfirmDeletes           bool
)

// mcpCmd registers the `corralctl mcp` subcommand. It runs a Model
// Context Protocol server over stdio so AI coding agents (Claude Code,
// Cursor, Cline, Codex CLI, etc.) can introspect the local Corral-
// organised workspace without making any network calls.
//
// Stdio is reserved for the JSON-RPC protocol stream by the MCP spec;
// every diagnostic this command emits goes to stderr.
var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run the corral-mcp server (Model Context Protocol over stdio, HTTP or SSE).",
	Long: `Start the Corral MCP server on stdio, or over HTTP with --transport.

The server exposes the local Corral-organised workspace (cloned
repositories under the configured base directory) to AI coding agents
through eight read-only tools and four resources. No network calls are
made and no forge API is contacted.

Tools:
  corral_find_symbol       - where a symbol is defined, across EVERY clone
  corral_search_code       - where text appears, across EVERY clone
  corral_repo_overview     - one repository's shape in a single call
  corral_list_repos        - filter clones by visibility/language/name
  corral_find_repo         - resolve a fuzzy name to one clone
  corral_get_repo_metadata - detailed info incl. current branch
  corral_status_summary    - aggregate counts by visibility + language
  corral_workspace_index   - full workspace index as JSON

corral_find_symbol is the one a single-repository code index cannot
offer: it resolves a function, method, type, interface, constant or
variable across the whole workspace at once. Go, Python, TypeScript,
JavaScript and Rust sources are indexed.

corral_search_code is its counterpart: find_symbol answers where
something is declared, search_code answers where it is written - call
sites, configuration keys, the error string from a ticket. Only files
the file resource would serve are searched, so a credential file can
never match.

Write tools, registered only with --enable-mutations, and audited:
  corral_sync_repo         - git pull one clone
  corral_clone_repo        - clone into the workspace
  corral_delete_repo       - remove one clone; additionally requires
                             --enable-destructive-mutations, and refuses
                             when the clone holds uncommitted, unpushed,
                             stashed, gitignored or submodule work

Those refusals stop mistakes, not intent: an agent talked into deleting
the one clone with no unpublished work passes every check. So each
deletion is also put to a person over MCP elicitation before it runs.
A client that cannot ask its user anything cannot delete.
--no-confirm-deletes removes this, and suits only an unattended
workspace you are willing to lose.

Transport:
  Default is stdio - the client launches this process and talks to it
  over the pipe; there is no listening port. The other two listen on
  --host and --port, for a client that connects to a server somebody
  else started:

    --transport streamable-http   Streamable HTTP at /mcp. One endpoint
                                  speaks both current protocol revisions:
                                  2026-07-28 (stateless, server/discover)
                                  and 2025-11-25 (initialize and
                                  Mcp-Session-Id).
    --transport sse               The legacy HTTP+SSE transport
                                  (protocol 2024-11-05) at /sse, for a
                                  host that still expects it.

  --http HOST:PORT is the older spelling of --transport streamable-http
  and still works; --host and --port are the way to say it now.

  The address must be on loopback. This server has no authentication
  and exposes every repository under its root, so --host 0.0.0.0 - or
  --http :7777, which binds every interface - is refused.
  --allow-remote overrides that, for an operator who has put their own
  authentication in front.

Resources:
  corral://workspace/index
  corral://repo/{owner}/{name}/state
  corral://repo/{owner}/{name}/tree
  corral://repo/{owner}/{name}/file/{path}

Install in Claude Code:
  claude mcp add corral -- corralctl mcp

Install in Cursor / Cline (mcp.json snippet):
  {
    "mcpServers": {
      "corral": {
        "command": "corralctl",
        "args": ["mcp"]
      }
    }
  }`,
	RunE: runMCP,
}

// mcpServer is the subset of the internal/mcp.Server API runMCP touches.
// Extracted as an interface so the unit test can stand up a stub without
// spinning up a real stdio server that would block forever on the
// test's os.Stdin.
type mcpServer interface {
	Root() string
	MutationsEnabled() bool
	AuditLogPath() string
	ServeStdio() error
	ServeHTTP(ctx context.Context, addr string) error
	ServeSSE(ctx context.Context, addr string) error
}

// mcpNewServer is indirected through a package var so unit tests can
// exercise runMCP's validation, wiring, and error-propagation paths
// without depending on the mcp-go library's stdio loop. Production
// callers get the real constructor.
var mcpNewServer = func(opts mcp.ServerOptions) (mcpServer, error) {
	return mcp.NewServer(opts)
}

var (
	absMCPPath = filepath.Abs
	statMCP    = os.Stat
)

func runMCP(cmd *cobra.Command, args []string) error {
	root := mcpRoot
	if root == "" {
		root = baseDir
	}
	if root == "" {
		root = defaultBaseDir()
	}
	abs, err := absMCPPath(root)
	if err != nil {
		return fmt.Errorf("resolving root %q: %w", root, err)
	}
	info, err := statMCP(abs)
	if err != nil {
		return fmt.Errorf("root %q is not accessible: %w", abs, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("root %q is not a directory", abs)
	}

	kind, addr, err := resolveTransport(mcpTransport, mcpHTTP, mcpHost, mcpPort)
	if err != nil {
		return err
	}
	if kind != transportStdio && !mcpAllowRemote && !loopbackOnly(addr) {
		return fmt.Errorf(
			"listening on %s would accept connections from other machines, and this server "+
				"has no authentication: it exposes every repository under %s, and with "+
				"mutations enabled it can change them.\n\n"+
				"Use a loopback address (--host 127.0.0.1 or --host localhost), or pass "+
				"--allow-remote if you have put your own authentication in front of it",
			addr, abs)
	}

	srv, err := mcpNewServer(mcp.ServerOptions{
		Root:                       abs,
		Version:                    Version,
		EnableMutations:            mcpEnableMutations,
		EnableDestructiveMutations: mcpEnableDestructiveMutations,
		ConfirmDeletes:             !mcpNoConfirmDeletes,
		AuditLogPath:               mcpAuditLog,
		AllowFileExts:              parseCSV(mcpAllowFileExts),
		SymbolCacheDir:             mcpSymbolCache,
	})
	if err != nil {
		return fmt.Errorf("constructing mcp server: %w", err)
	}

	// Startup banner on stderr — stdout is the protocol stream.
	// The audit-log path is surfaced so an operator setting up the flow
	// can immediately grep it for the first mutation.
	auditNote := "off"
	if p := srv.AuditLogPath(); p != "" {
		auditNote = p
	}
	fmt.Fprintf(os.Stderr, "corral-mcp %s starting; root=%s mutations=%t destructive=%t audit=%s\n",
		version.Display(Version), srv.Root(), srv.MutationsEnabled(), mcpEnableDestructiveMutations, auditNote)

	if mcpHTTP != "" {
		fmt.Fprintf(os.Stderr, "corral-mcp: --http is deprecated; use --transport streamable-http --host HOST --port PORT\n")
	}

	var serveErr error
	switch kind {
	case transportStreamableHTTP:
		fmt.Fprintf(os.Stderr, "corral-mcp listening on http://%s%s (streamable HTTP; protocol 2026-07-28 and 2025-11-25)\n",
			addr, mcp.StreamableEndpoint)
		serveErr = srv.ServeHTTP(cmdContext(cmd), addr)
	case transportSSE:
		fmt.Fprintf(os.Stderr, "corral-mcp listening on http://%s%s (legacy HTTP+SSE; protocol 2024-11-05)\n",
			addr, mcp.SSEEndpoint)
		serveErr = srv.ServeSSE(cmdContext(cmd), addr)
	default:
		serveErr = srv.ServeStdio()
	}
	if serveErr != nil {
		return fmt.Errorf("mcp server: %w", serveErr)
	}
	return nil
}

// transportKind is which of the three ways in the server was asked for.
type transportKind string

const (
	transportStdio          transportKind = "stdio"
	transportStreamableHTTP transportKind = "streamable-http"
	transportSSE            transportKind = "sse"
)

// resolveTransport folds --transport, --host, --port and the older --http
// into one decision: which transport, and for a listening one, the address.
//
// --http predates the other three and is kept so a configured client keeps
// working. It names the streamable transport and its address in one flag,
// so it cannot be combined with --transport sse, and it takes the address
// over --host/--port rather than merging with them: a user who typed
// 127.0.0.1:7777 meant that, not port 7777 on whatever --host defaults to.
func resolveTransport(transport, httpAddr, host string, port int) (transportKind, string, error) {
	kind := transportKind(transport)
	switch kind {
	case transportStdio, transportStreamableHTTP, transportSSE:
	default:
		return "", "", fmt.Errorf("--transport %q is not one of stdio, streamable-http, sse", transport)
	}
	if httpAddr != "" {
		if kind == transportSSE {
			return "", "", fmt.Errorf("--http names the streamable-http transport and cannot be combined with --transport sse")
		}
		return transportStreamableHTTP, httpAddr, nil
	}
	if kind == transportStdio {
		return transportStdio, "", nil
	}
	if port < 1 || port > 65535 {
		return "", "", fmt.Errorf("--port %d is not a TCP port (1-65535)", port)
	}
	if host == "" {
		return "", "", fmt.Errorf("--host must name an interface; an empty host binds every one")
	}
	return kind, net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// loopbackOnly reports whether addr binds only to the local machine.
//
// The server has no authentication and exposes a developer's entire
// workspace — and, with mutations on, writes to it. Binding that to a
// routable interface publishes it, so a non-loopback address has to be
// asked for rather than arrived at by typing ":7777" and not thinking
// about the empty host.
func loopbackOnly(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	switch host {
	case "localhost":
		return true
	case "":
		// ":7777" binds every interface, which is the case most likely to
		// be typed by accident.
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func init() {
	mcpCmd.Flags().StringVar(&mcpRoot, "root", "", "absolute path the server sandboxes itself to (defaults to --base-dir, then $HOME/Code)")
	mcpCmd.Flags().BoolVar(&mcpEnableMutations, "enable-mutations", false, "unlock write tools (corral_sync_repo, corral_clone_repo). Every mutation is logged to the audit trail")
	mcpCmd.Flags().BoolVar(&mcpEnableDestructiveMutations, "enable-destructive-mutations", false, "additionally unlock corral_delete_repo. Refuses when uncommitted or unpushed changes exist. Requires --enable-mutations")
	mcpCmd.Flags().StringVar(&mcpTransport, "transport", string(transportStdio),
		"how clients reach the server: stdio, streamable-http (/mcp) or sse (/sse, the legacy HTTP+SSE transport)")
	mcpCmd.Flags().StringVar(&mcpHost, "host", "127.0.0.1",
		"interface to listen on for an HTTP transport")
	mcpCmd.Flags().IntVar(&mcpPort, "port", 8000,
		"TCP port to listen on for an HTTP transport")
	mcpCmd.Flags().StringVar(&mcpHTTP, "http", "",
		"deprecated: serve Streamable HTTP on this HOST:PORT; the same as --transport streamable-http with --host and --port")
	mcpCmd.Flags().BoolVar(&mcpAllowRemote, "allow-remote", false,
		"permit a non-loopback --host or --http address. The server has no authentication; only use this behind your own")
	mcpCmd.Flags().BoolVar(&mcpNoConfirmDeletes, "no-confirm-deletes", false,
		"delete without asking a person to confirm. Only for an unattended workspace you are willing to lose")
	mcpCmd.Flags().StringVar(&mcpAllowFileExts, "allow-file-ext", "",
		"comma-separated extra file extensions the file resource may serve (e.g. \"tpl,hbs\"). Cannot re-enable credential files")
	mcpCmd.Flags().StringVar(&mcpSymbolCache, "symbol-cache", "",
		"where to persist the symbol index between runs (defaults to $XDG_CACHE_HOME/corral/symbols; \"off\" disables it)")
	mcpCmd.Flags().StringVar(&mcpAuditLog, "audit-log", "", "path to the mutation audit log (defaults to $XDG_STATE_HOME/corral/mutations.log or ~/.local/state/corral/mutations.log)")
	rootCmd.AddCommand(mcpCmd)
}
