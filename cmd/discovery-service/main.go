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
// The build line goes out first, before configuration is read: the most common
// question about a service that failed to start is which build failed, and an
// error from a binary that never said what it was is one nobody can place.
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
	// Closes the pool and flushes the logger, on the shutdown path and the
	// serve-failed path alike. app.Run does not return until the listener is
	// closed and no handler is running, so nothing still holds a connection when
	// this fires.
	defer application.Close()

	return app.Run(ctx, application)
}

// writeBuildInfo reports the module, version and VCS revision this binary was
// linked from.
//
// Read from the toolchain's own build stamp rather than injected with -ldflags,
// so Makefile, Dockerfile and CI need not agree on a flag string for a binary to
// identify itself. That preference is repo-wide, and the telemetry Resource is
// the exception: all four of its build attributes ARE injected, because the
// release image's build context carries no .git and the toolchain therefore
// writes no vcs.* settings into it. Why: docs/design/opentelemetry.md, "Build
// identity". Do not restate it here — it was wrong at four sites until it was
// measured.
//
// This line keeps the free route and so still prints `unknown` for the revision
// inside a release image. That is a smaller surface than the Resource — an
// operator's `--version` rather than every span a facilitator receives — and
// wiring it to the linker stamp would mean exporting telemetry's readBuild for
// one print.
//
// What this line prints differs between the two builds: in a git checkout on
// go1.25 Main.Version reads a pseudo-version derived from the last tag, and in
// the .git-less release image it reads `(devel)`.
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

// vcsRevision returns the commit the binary was built from.
//
// A build from an exported tree — and every `go test` binary — carries no VCS
// stamp, so the absence is reported as a value rather than an error.
func vcsRevision(info *debug.BuildInfo) string {
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return "unknown"
}
