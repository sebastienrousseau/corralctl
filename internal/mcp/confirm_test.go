// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// per-call authority (SEC-4)
// ---------------------------------------------------------------------------

// stubConfirmer stands in for a person where the outcome, not the protocol
// plumbing, is what a test is asserting. It answers on the first pass, so
// the handler is driven straight to the decision.
type stubConfirmer struct {
	decision confirmDecision
	calls    int
	summary  string
	detail   string
}

func (c *stubConfirmer) Confirm(_ *mcp.CallToolRequest, summary, detail string) (confirmDecision, *mcp.CallToolResult) {
	c.calls++
	c.summary, c.detail = summary, detail
	if c.decision == confirmAsk {
		return confirmAsk, &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{
				confirmRequestID: &mcp.ElicitParams{Mode: "form", Message: summary},
			},
		}
	}
	return c.decision, nil
}

// confirmHarness wires a delete-enabled server over one clean clone, with
// the refusal cascade stubbed to "nothing to protect" so the confirmation is
// the only thing left between the call and the removal.
func confirmHarness(t *testing.T, confirm bool, c confirmer) (*harness, string) {
	t.Helper()
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "doomed", "https://github.com/acme/doomed.git", "")
	safeGuards(t)

	h := newHarness(t, ServerOptions{
		Root:                       base,
		EnableMutations:            true,
		EnableDestructiveMutations: true,
		ConfirmDeletes:             confirm,
		AuditLogPath:               filepath.Join(t.TempDir(), "audit.log"),
	})
	if c != nil {
		h.server.confirmer = c
	}
	return h, repo
}

// TestDeleteRequiresConfirmation is the SEC-4 property. The refusal cascade
// stops mistakes; this is what stops a persuaded agent picking the one clone
// that passes every check.
func TestDeleteRequiresConfirmation(t *testing.T) {
	c := &stubConfirmer{decision: confirmDenied}
	h, repo := confirmHarness(t, true, c)

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
	if !isErr {
		t.Fatalf("a declined deletion must be reported as an error: %s", out)
	}
	if c.calls != 1 {
		t.Errorf("confirmer called %d times, want 1", c.calls)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Error("the clone was deleted despite being declined")
	}
	if !strings.Contains(out, "declined") {
		t.Errorf("the refusal should say it was declined: %s", out)
	}
}

func TestDeleteProceedsWhenApproved(t *testing.T) {
	c := &stubConfirmer{decision: confirmApproved}
	h, repo := confirmHarness(t, true, c)

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
	if isErr {
		t.Fatalf("an approved deletion should succeed: %s", out)
	}
	if c.calls != 1 {
		t.Errorf("confirmer called %d times, want 1", c.calls)
	}
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		t.Error("the clone should have been removed")
	}
}

// TestDeleteFailsClosedWhenNobodyCanBeAsked: if the question cannot be put,
// nobody has approved. Proceeding because asking failed would make the gate
// worthless against exactly the client that ignores it.
func TestDeleteFailsClosedWhenNobodyCanBeAsked(t *testing.T) {
	c := &stubConfirmer{decision: confirmUnavailable}
	h, repo := confirmHarness(t, true, c)

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
	if !isErr {
		t.Fatalf("an unaskable confirmation must refuse, not proceed: %s", out)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Error("the clone was deleted without approval")
	}
	// The refusal has to name the way out, or an operator is simply stuck.
	if !strings.Contains(out, "--no-confirm-deletes") {
		t.Errorf("the refusal should name the escape hatch: %s", out)
	}
}

// TestDeleteWithoutConfirmationWhenDisabled covers the opt-out, which exists
// for an unattended workspace.
func TestDeleteWithoutConfirmationWhenDisabled(t *testing.T) {
	c := &stubConfirmer{decision: confirmDenied} // would refuse, but is never asked
	h, repo := confirmHarness(t, false, c)

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
	if isErr {
		t.Fatalf("with confirmation off the deletion should proceed: %s", out)
	}
	if c.calls != 0 {
		t.Errorf("the confirmer was called %d times with confirmation disabled", c.calls)
	}
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		t.Error("the clone should have been removed")
	}
}

// TestDeleteAsksOnlyAfterTheCascade: a deletion the server would refuse on
// its own must never reach a person. Prompting for something that was going
// to be declined anyway is how people learn to click through prompts.
func TestDeleteAsksOnlyAfterTheCascade(t *testing.T) {
	c := &stubConfirmer{decision: confirmApproved}
	h, repo := confirmHarness(t, true, c)
	stubSeam(t, &hasUnpushedCommits, func(context.Context, string) (bool, string) {
		return true, "3 commits reachable only from local refs"
	})

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
	if !isErr {
		t.Fatalf("unpushed work must still refuse: %s", out)
	}
	if c.calls != 0 {
		t.Errorf("a deletion the cascade refuses must not reach a person (asked %d times)", c.calls)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Error("the clone was deleted")
	}
}

// TestConfirmationMessageIsUseful: a prompt that does not say what will be
// destroyed cannot be answered responsibly.
func TestConfirmationMessageIsUseful(t *testing.T) {
	c := &stubConfirmer{decision: confirmDenied}
	h, _ := confirmHarness(t, true, c)
	_, _ = h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})

	if !strings.Contains(c.summary, "doomed") {
		t.Errorf("the summary should name the repository: %q", c.summary)
	}
	for _, want := range []string{"Path:", "Origin:", "removes the directory"} {
		if !strings.Contains(c.detail, want) {
			t.Errorf("the detail should contain %q: %q", want, c.detail)
		}
	}
}

// TestDeleteReportsAuditFailureAroundConfirmation covers the two audit
// writes inside the confirmation block. A refusal that is not recorded is
// invisible to whoever reviews the log afterwards, so the tool says so
// rather than reporting a clean decline.
func TestDeleteReportsAuditFailureAroundConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    *stubConfirmer
	}{
		{"declined", &stubConfirmer{decision: confirmDenied}},
		{"unaskable", &stubConfirmer{decision: confirmUnavailable}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo := confirmHarness(t, true, tc.c)
			failingAudit(t)

			out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
			if !isErr {
				t.Fatalf("the deletion should still be refused: %s", out)
			}
			if !strings.Contains(out, "audit failed") {
				t.Errorf("the audit failure was swallowed: %s", out)
			}
			if _, err := os.Stat(repo); err != nil {
				t.Error("the clone was deleted")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// the elicitation confirmer, over a real client
// ---------------------------------------------------------------------------

// elicitHarness connects a client whose elicitation handler answers with
// action. Leaving both action and handlerErr empty leaves the handler unset,
// which is how a client that cannot ask its user anything presents itself.
func elicitHarness(t *testing.T, action string, handlerErr error) (*harness, string, *int) {
	t.Helper()
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "doomed", "https://github.com/acme/doomed.git", "")
	safeGuards(t)

	asked := 0
	var clientOpts *mcp.ClientOptions
	if action != "" || handlerErr != nil {
		clientOpts = &mcp.ClientOptions{
			ElicitationHandler: func(_ context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				asked++
				if handlerErr != nil {
					return nil, handlerErr
				}
				return &mcp.ElicitResult{Action: action}, nil
			},
		}
	}

	h := newHarnessWithClient(t, ServerOptions{
		Root:                       base,
		EnableMutations:            true,
		EnableDestructiveMutations: true,
		ConfirmDeletes:             true,
		AuditLogPath:               filepath.Join(t.TempDir(), "audit.log"),
	}, clientOpts)
	return h, repo, &asked
}

// TestElicitationDecidesTheDeletion drives the real confirmer end to end:
// the server asks over the protocol, the client answers, and the answer
// decides whether a directory still exists.
func TestElicitationDecidesTheDeletion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		action  string
		deleted bool
	}{
		{"accept", "accept", true},
		// Neither of these is consent. "cancel" in particular is a user
		// who dismissed the prompt without choosing, and treating that as
		// approval is the classic dialog bug.
		{"decline", "decline", false},
		{"cancel", "cancel", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo, asked := elicitHarness(t, tc.action, nil)

			out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
			if *asked != 1 {
				t.Errorf("the client was asked %d times, want 1", *asked)
			}
			_, statErr := os.Stat(repo)
			if tc.deleted {
				if isErr {
					t.Fatalf("an accepted deletion should succeed: %s", out)
				}
				if !os.IsNotExist(statErr) {
					t.Error("the clone should have been removed")
				}
				return
			}
			if !isErr {
				t.Fatalf("%q is not approval: %s", tc.action, out)
			}
			if statErr != nil {
				t.Errorf("the clone was deleted on %q", tc.action)
			}
		})
	}
}

// TestElicitationUnsupportedClientCannotDelete is the capability check. A
// client that never declared elicitation would reject or ignore the request,
// so asking anyway would hang the call rather than refuse it.
func TestElicitationUnsupportedClientCannotDelete(t *testing.T) {
	h, repo, asked := elicitHarness(t, "", nil)

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
	if !isErr {
		t.Fatalf("a client that cannot ask anyone must not be able to delete: %s", out)
	}
	if *asked != 0 {
		t.Errorf("a client without a handler was asked %d times", *asked)
	}
	if !strings.Contains(out, "does not support elicitation") {
		t.Errorf("the refusal should explain why: %s", out)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Error("the clone was deleted")
	}
}

// TestElicitationTransportErrorFailsClosed covers the last of the three ways
// a confirmation can go unanswered: the client accepted the request and then
// failed to produce an answer.
//
// The SDK fulfils input requests in middleware, above the handler, so a
// client that fails there aborts the whole call with a protocol error rather
// than returning a tool result. That is a worse message than the refusal the
// handler would have written, but it is the same outcome that matters: the
// tool never reaches its second pass, and the clone is still there.
func TestElicitationTransportErrorFailsClosed(t *testing.T) {
	h, repo, asked := elicitHarness(t, "", errors.New("client exploded"))

	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "corral_delete_repo",
		Arguments: map[string]any{"query": "doomed"},
	})
	if err == nil {
		t.Fatalf("a failed confirmation must not complete the call: %+v", res)
	}
	if !strings.Contains(err.Error(), "client exploded") {
		t.Errorf("the cause should survive: %v", err)
	}
	if *asked != 1 {
		t.Errorf("the client was asked %d times, want 1", *asked)
	}
	if _, statErr := os.Stat(repo); statErr != nil {
		t.Error("the clone was deleted")
	}
}

func TestElicitConfirmerWithoutASession(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *mcp.CallToolRequest
	}{
		{"no request", nil},
		{"no session", &mcp.CallToolRequest{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ask := (elicitConfirmer{}).Confirm(tc.req, "s", "d")
			if got != confirmUnavailable {
				t.Errorf("decision = %v, want confirmUnavailable", got)
			}
			if ask != nil {
				t.Error("there is nothing to ask when there is nobody to ask")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// per-repository locking
// ---------------------------------------------------------------------------

func TestRepoLocksArePerPath(t *testing.T) {
	l := newRepoLocks()
	a, again, b := l.lock("/a"), l.lock("/a"), l.lock("/b")
	if a != again {
		t.Error("the same path must yield the same lock")
	}
	if a == b {
		t.Error("different paths must not share a lock")
	}
}

// TestRepoLocksSerialise proves the lock actually excludes, rather than
// merely existing.
func TestRepoLocksSerialise(t *testing.T) {
	l := newRepoLocks()
	var (
		mu      sync.Mutex
		inside  int
		maxSeen int
	)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := l.lock("/same")
			m.Lock()
			defer m.Unlock()
			mu.Lock()
			inside++
			if inside > maxSeen {
				maxSeen = inside
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			inside--
			mu.Unlock()
		}()
	}
	wg.Wait()
	if maxSeen != 1 {
		t.Errorf("saw %d holders at once; the lock does not exclude", maxSeen)
	}
}

// TestRepoLocksAreConcurrencySafe exercises the map itself under -race: many
// goroutines racing to create and take locks over a handful of paths.
func TestRepoLocksAreConcurrencySafe(t *testing.T) {
	l := newRepoLocks()
	const paths, workers = 8, 64

	var mu sync.Mutex
	held := map[string]int{}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := "/repo/" + itoaMCP(i%paths)
			m := l.lock(path)
			m.Lock()
			defer m.Unlock()
			mu.Lock()
			held[path]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if len(held) != paths {
		t.Errorf("locked %d distinct paths, want %d", len(held), paths)
	}
	for path, n := range held {
		if want := workers / paths; n != want {
			t.Errorf("%s was held %d times, want %d", path, n, want)
		}
	}
}

// ---------------------------------------------------------------------------
// the HTTP transport
// ---------------------------------------------------------------------------

// freePort asks the kernel for a port nothing is using, so a busy machine
// cannot make these tests flaky.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// waitForListener blocks until addr accepts a connection, rather than
// sleeping a fixed amount and hoping a loaded machine kept up.
func waitForListener(t *testing.T, addr string) {
	t.Helper()
	var err error
	for i := 0; i < 200; i++ {
		var conn net.Conn
		if conn, err = net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the server never accepted a connection on %s: %v", addr, err)
}

// TestServeHTTPServesAndShutsDown covers the transport end to end: a real
// JSON-RPC call over HTTP, and a clean shutdown on context cancellation.
func TestServeHTTPServesAndShutsDown(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/acme/alpha.git", "")

	s, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}

	addr := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ServeHTTP(ctx, addr) }()

	waitForListener(t, addr)

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
		`"params":{"name":"corral_status_summary","arguments":{}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr, body)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("request failed: %v", err)
	}
	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	_ = resp.Body.Close()
	if got := string(buf[:n]); !strings.Contains(got, `\"total\": 1`) {
		t.Errorf("the tool did not answer over HTTP: %q", got)
	}

	// Cancelling the context must shut the listener down and return nil: a
	// clean stop is not an error, and reporting one would make a supervised
	// process look like it crashed every time it was stopped.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("shutdown returned %v, want nil", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ServeHTTP did not return after its context was cancelled")
	}

	// And the port is genuinely released.
	if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Error("the listener is still accepting connections after shutdown")
	}

	// The request above went through http.DefaultClient, which keeps the
	// connection alive for reuse. Nothing will reuse it — the server it
	// points at has just been shut down — so the writeLoop goroutine behind
	// it would sit there until the process exits, which goleak reports as a
	// leak because that is what it is.
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
}

// TestServeHTTPReportsAListenFailure covers the error path: a port already
// taken must surface rather than hang.
func TestServeHTTPReportsAListenFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	s, err := NewServer(ServerOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ServeHTTP(context.Background(), ln.Addr().String()); err == nil {
		t.Error("binding an occupied port should return an error")
	}
}

// TestInstructionsMentionConfirmation: a model told nothing about the
// confirmation reports a declined delete as a tool failure rather than as a
// person saying no.
func TestInstructionsMentionConfirmation(t *testing.T) {
	with := serverInstructions(ServerOptions{
		Root: "/tmp", EnableMutations: true,
		EnableDestructiveMutations: true, ConfirmDeletes: true,
	})
	if !strings.Contains(with, "requires a person to confirm") {
		t.Errorf("the instructions should mention confirmation: %q", with)
	}

	without := serverInstructions(ServerOptions{
		Root: "/tmp", EnableMutations: true,
		EnableDestructiveMutations: true, ConfirmDeletes: false,
	})
	if strings.Contains(without, "requires a person to confirm") {
		t.Error("the instructions should not promise confirmation when it is disabled")
	}
}

// TestServeHTTPTreatsAClosedServerAsACleanStop pins the branch that keeps a
// supervised process from reporting a crash every time it is stopped: a
// listener that ends because the server was closed has not failed.
func TestServeHTTPTreatsAClosedServerAsACleanStop(t *testing.T) {
	stubSeam(t, &listenAndServe, func(*http.Server) error { return http.ErrServerClosed })

	s, err := NewServer(ServerOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ServeHTTP(context.Background(), freePort(t)); err != nil {
		t.Errorf("a closed server is not an error, got %v", err)
	}
}

// TestConfirmationSurvivesTheHTTPTransport is the deployment shape, not a
// unit: a real client over Streamable HTTP, against a stateless server.
//
// It is here because the stateless transport is the one thing that could
// quietly disable the confirmation. Statelessness means each request is
// served on its own session, so if the negotiated capabilities did not
// survive, every deletion would refuse with "this client does not support
// elicitation" — a gate that fails closed, but for the wrong reason, and
// one that no stdio test would notice.
func TestConfirmationSurvivesTheHTTPTransport(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "doomed", "https://github.com/acme/doomed.git", "")
	safeGuards(t)

	s, err := NewServer(ServerOptions{
		Root:                       base,
		EnableMutations:            true,
		EnableDestructiveMutations: true,
		ConfirmDeletes:             true,
		AuditLogPath:               filepath.Join(t.TempDir(), "audit.log"),
	})
	if err != nil {
		t.Fatal(err)
	}

	addr := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.ServeHTTP(ctx, addr) }()
	waitForListener(t, addr)

	// Cancelling is not the same as having stopped. ServeHTTP returns only
	// once http.Server.Shutdown has drained its connections, and this test
	// used to cancel and walk away — so on a slow machine the shutdown was
	// still running after the last test in the package finished. goleak
	// caught it on CI within hours of being added, and never once here,
	// which is the whole argument for the check: a leak that depends on
	// timing is invisible on the machine that wrote it.
	//
	// The client's idle keep-alive connections have to go too. They are
	// held by the transport, not the session, so closing the session leaves
	// a writeLoop goroutine parked on a connection nothing will use again.
	defer func() {
		cancel()
		select {
		case err := <-served:
			t.Logf("ServeHTTP returned: %v", err)
		case <-time.After(10 * time.Second):
			t.Error("ServeHTTP did not return within 10s of the context being cancelled")
		}
		http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	}()

	asked := 0
	client := mcp.NewClient(&mcp.Implementation{Name: "http-harness", Version: "test"},
		&mcp.ClientOptions{
			ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				asked++
				if !strings.Contains(req.Params.Message, "doomed") {
					t.Errorf("the prompt does not say what is being deleted: %q", req.Params.Message)
				}
				return &mcp.ElicitResult{Action: "accept"}, nil
			},
		})

	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: "http://" + addr}, nil)
	if err != nil {
		t.Fatalf("connect over HTTP: %v", err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "corral_delete_repo",
		Arguments: map[string]any{"query": "doomed"},
	})
	if err != nil {
		t.Fatalf("call over HTTP: %v", err)
	}
	if res.IsError {
		var b strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		t.Fatalf("an approved deletion over HTTP should succeed: %s", b.String())
	}
	if asked != 1 {
		t.Errorf("the person was asked %d times over HTTP, want 1", asked)
	}
	if _, statErr := os.Stat(repo); !os.IsNotExist(statErr) {
		t.Error("the clone should have been removed")
	}
}

// ---------------------------------------------------------------------------
// staging (SEC-5)
// ---------------------------------------------------------------------------

// TestDeleteStagesBeforeRemoving is the SEC-5 property. Every guard runs
// against a path any other process can still write to; the rename is what
// makes the clone unreachable under the name a writer knew, and the second
// pass of the guards is what sees anything that landed in between.
func TestDeleteStagesBeforeRemoving(t *testing.T) {
	c := &stubConfirmer{decision: confirmApproved}
	h, repo := confirmHarness(t, true, c)

	var staged string
	stubSeam(t, &removeMutation, func(p string) error {
		staged = p
		return os.RemoveAll(p)
	})

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
	if isErr {
		t.Fatalf("a clean deletion should succeed: %s", out)
	}
	if staged == repo {
		t.Error("the clone was removed under its own path; nothing closed the window")
	}
	if !strings.HasPrefix(filepath.Base(staged), stagePrefix) {
		t.Errorf("staged path %q is not marked as machinery", staged)
	}
	// Compared through EvalSymlinks because the server resolves the path
	// it acts on and a temp directory is a symlink on macOS.
	wantParent, err := filepath.EvalSymlinks(filepath.Dir(repo))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(staged) != wantParent {
		t.Errorf("staged into %q, not the clone's own parent %q — a rename across "+
			"directories is not guaranteed atomic", filepath.Dir(staged), wantParent)
	}
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		t.Error("the original path should be gone")
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Error("the staged copy should be gone too")
	}
}

// TestDeleteRestoresWhenTheSecondCheckFails is the case staging exists for:
// work landed between the first check and the rename. Losing that race must
// cost nothing.
func TestDeleteRestoresWhenTheSecondCheckFails(t *testing.T) {
	c := &stubConfirmer{decision: confirmApproved}
	h, repo := confirmHarness(t, true, c)

	// Clean on the first pass, dirty on the second — exactly the race.
	calls := 0
	stubSeam(t, &hasDirtyWorkingTree, func(context.Context, string) (bool, string) {
		calls++
		if calls > 1 {
			return true, "M internal/thing.go"
		}
		return false, ""
	})
	stubSeam(t, &removeMutation, func(string) error {
		t.Error("nothing should be removed once the second check fails")
		return nil
	})

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
	if !isErr {
		t.Fatalf("work that appeared during staging must refuse: %s", out)
	}
	if !strings.Contains(out, "after staging") {
		t.Errorf("the refusal should say when it was detected: %s", out)
	}
	if !strings.Contains(out, "restored") {
		t.Errorf("the refusal should say the clone is safe: %s", out)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Errorf("the clone must be back where it was: %v", err)
	}
	// And the staged name must not be left behind.
	entries, err := os.ReadDir(filepath.Dir(repo))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stagePrefix) {
			t.Errorf("a staged directory was left behind: %s", e.Name())
		}
	}
}

// TestDeleteReportsAFailedRestore: the worst outcome is a clone sitting
// under a name nobody would look for, so it must be named in the error.
func TestDeleteReportsAFailedRestore(t *testing.T) {
	c := &stubConfirmer{decision: confirmApproved}
	h, _ := confirmHarness(t, true, c)

	calls := 0
	stubSeam(t, &hasUnpushedCommits, func(context.Context, string) (bool, string) {
		calls++
		return calls > 1, "2 commits reachable only from local refs"
	})
	stubSeam(t, &unstageRemoval, func(string, string) error {
		return errors.New("device busy")
	})

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
	if !isErr {
		t.Fatalf("expected a refusal: %s", out)
	}
	for _, want := range []string{"could NOT be restored", stagePrefix, "device busy", "by hand"} {
		if !strings.Contains(out, want) {
			t.Errorf("the error should contain %q so the clone can be found: %s", want, out)
		}
	}
}

func TestDeleteReportsAStagingFailure(t *testing.T) {
	c := &stubConfirmer{decision: confirmApproved}
	h, repo := confirmHarness(t, true, c)
	stubSeam(t, &stageForRemoval, func(string) (string, error) {
		return "", errors.New("read-only file system")
	})
	stubSeam(t, &removeMutation, func(string) error {
		t.Error("nothing should be removed when staging failed")
		return nil
	})

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
	if !isErr {
		t.Fatalf("expected a refusal: %s", out)
	}
	if !strings.Contains(out, "read-only file system") {
		t.Errorf("the cause should survive: %s", out)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Error("the clone must be untouched")
	}
}

// TestDeleteRestoresWhenRemovalFails: a removal that fails halfway leaves
// the clone staged, and leaving it under a hidden name would be worse than
// the failure itself.
func TestDeleteRestoresWhenRemovalFails(t *testing.T) {
	c := &stubConfirmer{decision: confirmApproved}
	h, repo := confirmHarness(t, true, c)
	stubSeam(t, &removeMutation, func(string) error {
		return errors.New("directory not empty")
	})

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
	if !isErr {
		t.Fatalf("expected an error: %s", out)
	}
	if !strings.Contains(out, "directory not empty") {
		t.Errorf("the cause should survive: %s", out)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Errorf("a failed removal must leave the clone where it was: %v", err)
	}
}

func TestStageForRemovalRefusesAnExistingName(t *testing.T) {
	// The only way a staging path exists already is a previous deletion
	// that crashed midway. Reusing it would delete whatever it holds.
	base := t.TempDir()
	dir := filepath.Join(base, "repo")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	stubSeam(t, &randRead, func(b []byte) (int, error) {
		for i := range b {
			b[i] = 0
		}
		return len(b), nil
	})
	first, err := stageForRemoval(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The same deterministic suffix, so the second attempt collides.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := stageForRemoval(dir); err == nil {
		t.Errorf("staging onto an existing path %q should fail", first)
	}
}

func TestStageForRemovalReportsARandomnessFailure(t *testing.T) {
	stubSeam(t, &randRead, func([]byte) (int, error) {
		return 0, errors.New("entropy pool exhausted")
	})
	if _, err := stageForRemoval(t.TempDir()); err == nil {
		t.Error("a staging name that cannot be made unique is a failure")
	}
}

func TestStageForRemovalReportsARenameFailure(t *testing.T) {
	if _, err := stageForRemoval(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("staging a path that does not exist should fail")
	}
}

// TestDeleteReportsAuditFailuresAroundStaging covers the arms where the
// audit log itself fails while a deletion is already in flight. An
// unloggable mutation is the one thing worse than a failed one, so each
// says so rather than reporting a clean outcome.
func TestDeleteReportsAuditFailuresAroundStaging(t *testing.T) {
	t.Run("staging failed", func(t *testing.T) {
		c := &stubConfirmer{decision: confirmApproved}
		h, _ := confirmHarness(t, true, c)
		stubSeam(t, &stageForRemoval, func(string) (string, error) {
			return "", errors.New("read-only file system")
		})
		failAuditAfter(t, 2)

		out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
		if !isErr {
			t.Fatalf("expected an error: %s", out)
		}
		if !strings.Contains(out, "audit completion failed") {
			t.Errorf("the audit failure was swallowed: %s", out)
		}
	})

	t.Run("second check failed", func(t *testing.T) {
		c := &stubConfirmer{decision: confirmApproved}
		h, _ := confirmHarness(t, true, c)
		calls := 0
		stubSeam(t, &hasIgnoredContent, func(context.Context, string) (bool, string) {
			calls++
			return calls > 1, ".env"
		})
		failAuditAfter(t, 2)

		out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
		if !isErr {
			t.Fatalf("expected a refusal: %s", out)
		}
		if !strings.Contains(out, "audit completion failed") {
			t.Errorf("the audit failure was swallowed: %s", out)
		}
	})

	t.Run("restore failed after a failed removal", func(t *testing.T) {
		c := &stubConfirmer{decision: confirmApproved}
		h, _ := confirmHarness(t, true, c)
		stubSeam(t, &removeMutation, func(string) error {
			return errors.New("directory not empty")
		})
		stubSeam(t, &unstageRemoval, func(string, string) error {
			return errors.New("device busy")
		})

		out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "doomed"})
		if !isErr {
			t.Fatalf("expected an error: %s", out)
		}
		// Both failures have to reach the caller: the removal that failed,
		// and the fact the clone is now under a name they will not find.
		for _, want := range []string{"directory not empty", "could not be restored", stagePrefix} {
			if !strings.Contains(out, want) {
				t.Errorf("the error should contain %q: %s", want, out)
			}
		}
	})
}
