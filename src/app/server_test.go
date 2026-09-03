package app

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap"
)

// Run wires net.Listen and the real signal-cancellable context ahead of
// serve, which every other test in this file drives directly with a
// hand-built listener. This is Run's own happy path — the actual production
// entrypoint, not merely serve one layer down.
func TestRunOpensTheConfiguredPortAndServesUntilCancelled(t *testing.T) {
	application := testApp(t, livePool{}, zap.NewNop())
	application.Config.Server.Port = 0 // the kernel picks a free one
	application.Config.Server.ShutdownTimeout = time.Second

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- Run(ctx, application) }()

	time.Sleep(50 * time.Millisecond) // give Run time to bind and start serving
	cancel()

	if err := <-served; err != nil {
		t.Errorf("Run: %v, want nil after a clean shutdown", err)
	}
}

// A port already in use is a boot failure and has to come back as one — the
// same claim TestAServeFailureIsReturnedRatherThanSwallowed makes for serve,
// but Run's own failure is at net.Listen, one layer above.
func TestRunReturnsAListenFailureRatherThanPanicking(t *testing.T) {
	listener := listenLocal(t)
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("Addr() = %T, want *net.TCPAddr", listener.Addr())
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("close the listener: %v", err)
		}
	})

	application := testApp(t, livePool{}, zap.NewNop())
	application.Config.Server.Port = address.Port

	if err := Run(context.Background(), application); err == nil {
		t.Error("Run answered nil while the configured port was already in use")
	}
}

// listenLocal opens a listener on a port the kernel picks, so the suite never
// collides with a developer's own service on 8080 and two of these can run in
// parallel.
func listenLocal(t *testing.T) net.Listener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return listener
}

// answer is one client's outcome, carried back off the goroutine that made the
// request — a t.Fatalf from there would stop the wrong goroutine.
type answer struct {
	status int
	err    error
}

func get(url string) <-chan answer {
	answers := make(chan answer, 1)
	go func() {
		response, err := http.Get(url) //nolint:noctx // the test's own deadline bounds it
		if err != nil {
			answers <- answer{err: err}
			return
		}
		closeErr := response.Body.Close()
		answers <- answer{status: response.StatusCode, err: closeErr}
	}()
	return answers
}

// The point of a graceful shutdown, and the only part of it a caller can
// observe: a request already inside a handler when SIGTERM lands is answered,
// not severed.
//
// Getting this wrong is not visible in development — a Close() instead of a
// Shutdown() looks identical to anyone whose requests finish in a millisecond.
// It shows up as a deploy that returns a scatter of connection resets to
// publishers whose catalogs were mid-write.
func TestShutdownCompletesAnInFlightRequest(t *testing.T) {
	listener := listenLocal(t)

	started := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusTeapot)
	})

	application := testApp(t, livePool{}, zap.NewNop())
	application.Config.Server.ShutdownTimeout = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, application, listener, handler) }()

	answers := get("http://" + listener.Addr().String() + "/slow")
	<-started

	// SIGTERM's stand-in, delivered while the handler is still inside itself.
	cancel()

	// The handler stays busy for a moment after the signal, and that moment is
	// the only window in which a graceful shutdown differs from an abrupt one.
	// Releasing it in the same breath as cancel() lets the request finish by
	// luck: a drain deadline derived from the context that just fired is
	// already expired, but Shutdown has nothing left to wait for by the time it
	// looks. The pause is what makes the deadline observable.
	time.Sleep(100 * time.Millisecond)
	close(release)

	got := <-answers
	if got.err != nil {
		t.Fatalf("the in-flight request was severed by the shutdown: %v", got.err)
	}
	if got.status != http.StatusTeapot {
		t.Errorf("status = %d, want %d", got.status, http.StatusTeapot)
	}
	if err := <-served; err != nil {
		t.Errorf("serve: %v, want nil after a clean shutdown", err)
	}
}

// The other half: the grace period is a deadline and not a promise. A handler
// still running when it expires has its connection closed, and serve says so
// rather than reporting the deploy as clean — an operator counting dropped
// requests against a release needs the process itself to admit it.
func TestAShutdownThatOutlastsItsGracePeriodIsReported(t *testing.T) {
	listener := listenLocal(t)

	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusTeapot)
	})

	application := testApp(t, livePool{}, zap.NewNop())
	application.Config.Server.ShutdownTimeout = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, application, listener, handler) }()

	get("http://" + listener.Addr().String() + "/stuck")
	<-started
	cancel()

	if err := <-served; err == nil {
		t.Error("serve reported a clean shutdown while a handler was still holding a connection")
	}
}

// A listener that cannot be served is a boot failure, and it has to come back
// as one: a Run that returned nil on a port already in use would exit 0 and let
// an orchestrator record the deployment as successful.
func TestAServeFailureIsReturnedRatherThanSwallowed(t *testing.T) {
	listener := listenLocal(t)
	if err := listener.Close(); err != nil {
		t.Fatalf("close the listener: %v", err)
	}

	err := serve(context.Background(), testApp(t, livePool{}, zap.NewNop()), listener,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err == nil {
		t.Error("serve returned nil over a closed listener")
	}
}
