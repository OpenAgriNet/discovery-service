package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go.uber.org/zap"
)

// readHeaderTimeout bounds how long a connection may spend sending its request
// line and headers.
//
// It is the one timeout that has to exist here rather than in config: a peer that
// dribbles headers forever holds a goroutine and a file descriptor without ever
// reaching a middleware, so neither RateLimit nor Envelope's body ceiling (C14)
// can touch it.
const readHeaderTimeout = 10 * time.Second

// Run serves until the process is signalled or ctx is cancelled.
//
// The listener is opened before the signal handler, so a port already in use fails
// the boot rather than being reported as a running service that answers nothing.
func Run(ctx context.Context, a *App) error {
	address := net.JoinHostPort("", strconv.Itoa(a.Config.Server.Port))

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	a.Log.Info("listening", zap.String("address", listener.Addr().String()))

	return serve(ctx, a, listener, NewRouter(a))
}

// serve is Run with the listener and the handler as parameters, which is what the
// lifecycle tests drive: a graceful shutdown is only observable against a handler
// still running when it starts, and the real route table cannot provide one.
func serve(ctx context.Context, a *App, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	// Buffered, so the goroutine can post ErrServerClosed and exit even when
	// nothing is left reading.
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	select {
	case err := <-served:
		// Serve stopped on its own, which here can only be a fault: the shutdown
		// path has not run, so ErrServerClosed is not among the possibilities.
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	a.Log.Info("draining", zap.Duration("grace", a.Config.Server.ShutdownTimeout))

	// WithoutCancel, because ctx is the thing that just fired: deriving the grace
	// period from an already-cancelled context would expire it immediately and turn
	// every graceful shutdown into an abrupt one.
	drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.Config.Server.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(drain); err != nil {
		// The grace period expired with handlers still running, and their
		// connections were closed under them. Reported rather than swallowed: a
		// non-zero exit is how an operator finds out requests were dropped.
		return fmt.Errorf("drain within %s: %w", a.Config.Server.ShutdownTimeout, err)
	}

	// Not required for correctness — the channel is buffered — but the wait is
	// what makes "Run returned" mean "the listener is closed and no handler is
	// running", which is what main relies on before it closes the pool.
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
