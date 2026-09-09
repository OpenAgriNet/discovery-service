// Command discovery-service is the OpenAgriNet Beckn v2.0.0 discover and
// publish service.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/OpenAgriNet/discovery-service/src/app"
	"github.com/OpenAgriNet/discovery-service/src/platform/config"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		// stderr and a non-zero exit, both. A boot failure that printed to
		// stdout and exited 0 is a container an orchestrator reports as
		// healthy.
		fmt.Fprintf(os.Stderr, "discovery-service: %v\n", err)
		os.Exit(1)
	}
}

// run is main with its effects as parameters, so the exit code is the only
// thing main itself decides.
//
// The build line goes out first, before configuration is even read: the most
// common question about a service that failed to start is which build failed,
// and an error printed by a binary that never said what it was is an error
// nobody can place.
func run(ctx context.Context, out io.Writer) error {
	if err := writeBuildInfo(out); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	application, err := app.Build(ctx, cfg)
	if err != nil {
		return err
	}
	// Closes the pool and flushes the logger, on the shutdown path and on the
	// serve-failed path alike. app.Run does not return until the listener is
	// closed and no handler is running, so nothing is still holding a
	// connection when this fires.
	defer application.Close()

	return app.Run(ctx, application)
}

// writeBuildInfo reports the module, version and VCS revision this binary was
// linked from, read from the toolchain's own build stamp rather than injected
// with -ldflags, so Makefile, Dockerfile and CI do not have to agree on a flag
// string for a binary to identify itself.
//
// That preference is repo-wide and has exactly one exception, which is not this
// line: src/platform/telemetry/identity.go's version IS injected, with a single
// -X. Why, and what the release image's stamp does not carry:
// docs/design/opentelemetry.md, "Build identity". Do not restate the reasoning
// here — it was wrong at four sites until it was measured on 2026-09-09.
//
// What this line in particular prints differs between the two builds, and that
// is worth knowing at the call site: in a git checkout on go1.25 Main.Version
// reads a pseudo-version derived from the last tag, and in the .git-less release
// image it reads `(devel)`. It used to say `(devel)` everywhere, which is true
// of only one of the two.
func writeBuildInfo(w io.Writer) error {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Errorf("read build info: not recorded in this binary")
	}

	_, err := fmt.Fprintf(w, "%s %s %s\n", info.Main.Path, info.Main.Version, vcsRevision(info))
	if err != nil {
		return fmt.Errorf("write build info: %w", err)
	}
	return nil
}

// vcsRevision returns the commit the binary was built from. A build from an
// exported tree — and every `go test` binary — carries no VCS stamp at all, so
// the absence is reported as a value rather than as an error.
func vcsRevision(info *debug.BuildInfo) string {
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return "unknown"
}
