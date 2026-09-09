package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/platform/buildinfo"
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

// TestWriteBuildInfoCarriesTheSameStampAsTheResource pins the boot line to the
// telemetry Resource rather than to debug.BuildInfo.
//
// It read Main.Version and vcs.revision until 2026-09-10, which meant the FIRST
// LINE of a released container's log was:
//
//	github.com/OpenAgriNet/discovery-service (devel) unknown
//
// `(devel)` because a .git-less build has no module version, and `unknown`
// because it has no VCS stamp either — the same root cause that left three
// build attributes empty on every exported span, showing up in the one place an
// operator looks first. Reading buildinfo.Read instead means the log line and
// the Resource cannot disagree about which build is running, which is the entire
// value of printing it.
//
// The whole line is compared, not each field searched for. Asserting the ABSENCE
// of `(devel)` and `unknown` is the tempting shape and it is wrong: this is a
// `go test` binary, which carries neither -ldflags nor a VCS stamp, so those are
// the correct output here and the assertion could only ever pass in a release
// build. What is checkable everywhere is that the line is the stamp — five
// fields, in order, whatever their values. Revert to Main.Version and the line
// is three fields and this fails.
func TestWriteBuildInfoCarriesTheSameStampAsTheResource(t *testing.T) {
	var buf bytes.Buffer

	if err := writeBuildInfo(&buf); err != nil {
		t.Fatalf("writeBuildInfo: %v", err)
	}
	got := strings.TrimSpace(buf.String())

	stamp := buildinfo.Read()
	want := strings.Join([]string{
		modulePath, stamp.Version, stamp.Commit, stamp.Date, stamp.TreeState,
	}, " ")

	if got != want {
		t.Errorf("build line\n got %q\nwant %q — the line and the telemetry Resource "+
			"must be assembled from the same stamp", got, want)
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
