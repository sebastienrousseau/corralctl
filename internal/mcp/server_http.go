// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sebastienrousseau/corralctl/internal/diag"
)

var serveStdio = serveStdioDefault

// listenMetrics is a package seam allowing tests to mock network listener creation.
var listenMetrics = net.Listen

// startMetricsServer starts a dedicated HTTP listener serving Prometheus
// telemetry on addr, protected with auth if AuthToken is set.
// It returns a channel that is closed when the server shuts down.
func (s *Server) startMetricsServer(ctx context.Context, addr string) <-chan struct{} {
	done := make(chan struct{})
	mux := http.NewServeMux()
	mux.Handle(MetricsEndpoint, s.metricsHandler())
	handler := s.wrapAuth(mux)

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
	}

	ln, err := listenMetrics("tcp", addr)
	if err != nil {
		diag.Warnf("corral-mcp: failed to listen on metrics addr %s: %v", addr, err)
		close(done)
		return done
	}

	go func() {
		defer close(done)
		stopWatch := make(chan struct{})
		defer close(stopWatch)
		go func() {
			select {
			case <-ctx.Done():
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdownCtx)
			case <-stopWatch:
				return
			}
		}()
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			diag.Warnf("corral-mcp: metrics server terminated: %v", err)
		}
	}()

	return done
}

// ServeStdio runs the server on the stdio transport (the MCP standard
// for local servers). Blocks until stdin closes or the server errors.
// Stdout is reserved for the JSON-RPC protocol stream — any debug logging
// the cmd layer wants to emit must go to stderr.
func (s *Server) ServeStdio() error {
	var metricsDone <-chan struct{}
	var stopMetrics context.CancelFunc
	if s.opts.MetricsAddr != "" {
		var metricsCtx context.Context
		metricsCtx, stopMetrics = context.WithCancel(context.Background())
		metricsDone = s.startMetricsServer(metricsCtx, s.opts.MetricsAddr)
	}
	if s.opts.WatchWorkspace {
		ctx, cancel := context.WithCancel(context.Background())
		watchDone := s.startWorkspaceWatcher(ctx)
		defer func() {
			cancel()
			<-watchDone
		}()
	}
	defer func() {
		if stopMetrics != nil {
			stopMetrics()
			<-metricsDone
		}
	}()
	return serveStdio(s.mcp)
}

// serveStdioDefault runs the server over the stdio transport. Split out so
// tests can stub the blocking call.
func serveStdioDefault(srv *mcp.Server) error {
	return srv.Run(context.Background(), &mcp.StdioTransport{})
}

// listenAndServe is indirected so a test can drive the branch below where
// the listener stops on its own. Today only Shutdown closes this server and
// Shutdown is reached through ctx.Done, so ErrServerClosed cannot arrive on
// the error channel in production — the guard is there so that a future
// caller adding a Close path does not turn an orderly stop into a crash
// report, and the seam is what keeps that guard from being an untested claim.
var listenAndServe = func(srv *http.Server) error { return srv.ListenAndServe() }

// ServeHTTP runs the server on the Streamable HTTP transport, the MCP
// standard for anything not launched as a subprocess. Both current protocol
// revisions are served on StreamableEndpoint; see eraRouter.
//
// Blocks until the listener fails or ctx is cancelled.
//
// The address should stay on loopback unless the caller has thought about it.
// This server reads a developer's whole workspace and, when mutations are
// enabled, writes to it; there is no authentication here, so binding it to a
// routable interface publishes that. The cmd layer refuses a non-loopback bind
// without an explicit flag, and this logs what it is listening on so an
// operator can see which it got.
func (s *Server) ServeHTTP(ctx context.Context, addr string) error {
	return s.serve(ctx, addr, s.httpHandler())
}

// serve is the listener behind ServeHTTP and ServeSSE: one http.Server, the
// given handler, the symbol warmer for as long as it runs, and an orderly
// stop when ctx is cancelled.
func (s *Server) serve(ctx context.Context, addr string, handler http.Handler) error {
	// Warm the symbol cache off the request path. HTTP only: this server is
	// long-lived and serves many sessions, so the scan pays for itself, while
	// a stdio server is started per session and may never be asked about
	// symbols at all. See warm.go.
	//
	// Its own cancellable context, not the caller's: if the listener fails,
	// this function returns while ctx is still live, and a warmer tied to ctx
	// would outlive the server it was warming. Waiting for it here means the
	// goroutine is always gone before ServeHTTP returns — which is what the
	// package's goroutine-leak check requires, and what stops a stopped server
	// from still reading the user's disk.
	warmCtx, stopWarm := context.WithCancel(ctx)
	warmDone := s.startSymbolWarmer(warmCtx)
	var watchDone <-chan struct{}
	var stopWatch context.CancelFunc
	if s.opts.WatchWorkspace {
		var watchCtx context.Context
		watchCtx, stopWatch = context.WithCancel(ctx)
		watchDone = s.startWorkspaceWatcher(watchCtx)
	}
	defer func() {
		if stopWatch != nil {
			stopWatch()
			<-watchDone
		}
		stopWarm()
		<-warmDone
		// After the warmer has stopped, the watcher has stopped (which it
		// does only once any warm pass it started has returned) and every
		// request has finished, no reference to a mapping can remain, so
		// this is the one safe point to release them.
		s.indexCache.closeAll()
	}()

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
		// A workspace scan on a large tree is the slowest thing here, and
		// a symbol extraction is slower still, so the read and write
		// budgets are generous. ReadHeaderTimeout is not: it is the one
		// that bounds a slow-loris connection, and no legitimate client
		// needs a second to send its headers.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	errs := make(chan error, 1)
	go func() { errs <- listenAndServe(srv) }()

	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// Give in-flight calls a moment to finish rather than cutting a
		// mutation off mid-write.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		// A 2025-11-25 session outlives the HTTP request that created it,
		// and Shutdown knows nothing about it: the session sits in the
		// transport's table with its goroutines until its idle timeout.
		// The listener is gone, so nothing can reach it again; end it now
		// rather than leave the process to exit around it.
		for session := range s.mcp.Sessions() {
			_ = session.Close()
		}
		return err
	}
}
