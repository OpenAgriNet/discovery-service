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
	"github.com/OpenAgriNet/discovery-service/src/platform/buildinfo"
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

// writeBuildInfo names the build this binary was linked from: module path,
// release tag, commit, commit date and tree state.
//
// It reads buildinfo.Read — the SAME four values the telemetry Resource is
// assembled from — so an operator reading the log and a facilitator reading a
// span cannot be told two different things about which build is running.
//
// It read debug.BuildInfo's Main.Version and vcs.revision until 2026-09-10, and
// so the first line of every release container's log was:
//
//	github.com/OpenAgriNet/discovery-service (devel) unknown
//
// `(devel)` because a .git-less build has no module version, `unknown` because
// it has no VCS stamp — the same cause that left three attributes empty on every
// exported span, surfacing in the place an operator looks first.
// docs/design/opentelemetry.md, "Build identity" holds the reasoning; do not
// restate it here — it was wrong at four sites until it was measured.
//
// Plain text rather than a zap line, which is a trade and not an oversight. run
// emits this BEFORE config.Load, because the most common question about a
// service that failed to start is which build failed, and the logger does not
// exist until app.Build. Making it structured would move it after the two steps
// most likely to fail — precisely when it is worth having.
//
// debug.ReadBuildInfo is still consulted, for the module path alone: that is
// the one field here the linker stamp does not carry, and it is the same in
// every build.
func writeBuildInfo(w io.Writer) error {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Errorf("read build info: not recorded in this binary")
	}

	build := buildinfo.Read()
	_, err := fmt.Fprintf(w, "%s %s %s %s %s\n",
		info.Main.Path, build.Version, build.Commit, build.Date, build.TreeState)
	if err != nil {
		return fmt.Errorf("write build info: %w", err)
	}
	return nil
}
