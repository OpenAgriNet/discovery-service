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
}
