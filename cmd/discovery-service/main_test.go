package main

import (
	"bytes"
	"strings"
	"testing"
)

const modulePath = "github.com/OpenAgriNet/discovery-service"

func TestWriteBuildInfoNamesTheModule(t *testing.T) {
	var buf bytes.Buffer

	if err := writeBuildInfo(&buf); err != nil {
		t.Fatalf("writeBuildInfo: %v", err)
	}

	got := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(got, modulePath+" ") {
		t.Errorf("build line %q does not start with the module path %q", got, modulePath)
	}
}

// A boot that fails has to fail as a returned error, so main is the only thing
// that decides an exit code — and the build line has to already be out, because
// "which build failed" is the first question asked of a service that did not
// start and an error from a binary that never named itself cannot answer it.
//
// The configuration this drives is unloadable by construction: config.Load
// resolves config/common.yaml relative to the working directory, and a test
// binary's working directory is its own package.
func TestRunReportsABootFailureAfterNamingTheBuild(t *testing.T) {
	var buf bytes.Buffer

	err := run(t.Context(), &buf)
	if err == nil {
		t.Fatal("run returned nil from a working directory with no configuration")
	}
	if !strings.HasPrefix(strings.TrimSpace(buf.String()), modulePath+" ") {
		t.Errorf("output %q does not name the build before the failure", buf.String())
	}

	// Which failure, not merely that there was one. This test's premise is that
	// no config/common.yaml is reachable from this package's directory; if that
	// ever stops holding, run() goes on to open a pool and then to bind a port
	// and block, and the test hangs until the whole suite times out instead of
	// going red. Naming the config path is what makes the premise part of what
	// is asserted.
	if !strings.Contains(err.Error(), "config/common.yaml") {
		t.Errorf("run failed with %v, want the missing configuration — the test's premise no longer holds", err)
	}
}
