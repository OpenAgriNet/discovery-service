// Command discovery-service is the OpenAgriNet Beckn v2.0.0 discover and
// publish service.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

func main() {
	if err := writeBuildInfo(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "discovery-service: %v\n", err)
		os.Exit(1)
	}
}

// writeBuildInfo reports the module, version and VCS revision this binary was
// linked from. The values are read from the toolchain's own build stamp rather
// than injected with -ldflags, so Makefile, Dockerfile and CI do not have to
// agree on a flag string for a deployed image to identify itself.
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
