// Package buildinfo answers one question — which build of this binary is
// running — for the two callers that ask it independently: the telemetry
// Resource, and cmd's boot line.
//
// It lives here rather than in src/platform/telemetry, which is where it was
// until 2026-09-10, because cmd may not import that package.
// tests/architecture/boundary_test.go permits src/platform/telemetry to
// src/app/container.go and five named files and nothing else, and its own
// comment argues that an allow-list entry nobody needs is an entry nobody will
// notice being used. A build stamp needs no OTel type to describe itself, so
// the exemption cmd would have received is one this package removes the need
// for. Nothing here imports the SDK, and nothing here should.
//
// The split also says the true thing about ownership: the stamp is not a
// telemetry concept. telemetry projects it onto a Resource, cmd prints it, and
// neither owns it.
package buildinfo

import "runtime/debug"

// version is injected at link time; docs/design/opentelemetry.md, "Build
// identity", says why it is the one attribute that cannot read the toolchain's
// build stamp. Do not restate that reasoning here — it was wrong at four sites
// until it was measured.
//
// `dev` rather than "" so an unset value differs from a dropped one. Pinned by
// tests/architecture/ldflags_test.go, because `go build -X` on a symbol that
// does not exist succeeds silently.
var version = "dev"

// The other three, empty until the linker fills them.
//
// They read from the toolchain's VCS stamp where there is one, and there is one
// for every build that happens inside a git working tree — which is every build
// EXCEPT the release image, whose context is a copy with no .git in it. So the
// three attributes that identify which commit is deployed were `unknown`,
// `unknown` and the epoch on precisely the binaries nobody can identify by
// looking at their own tree. The stamp now crosses as -ldflags and the VCS
// settings override it where they exist, which keeps a local build honest about
// a dirty tree the build system would have no way to know about.
//
// Four separate `var x = ""` declarations and not one grouped block:
// tests/architecture/ldflags_test.go resolves each -X target back to its
// declaration, and `go build -X` on a symbol that does not exist succeeds
// silently, so the test reads the source rather than trusting the flag.
var commit = ""
var buildDate = ""
var treeState = ""

// Stamp is what -ldflags and the toolchain's VCS stamp know between them about
// the binary that is running.
type Stamp struct {
	Version   string
	Commit    string
	TreeState string
	Date      string
}

// The values the three VCS-derived attributes take when the binary carries no
// stamp — every `go test` binary, and every build from an exported tree that
// the build system did not stamp either.
const (
	unknownRevision  = "unknown"
	unknownTreeState = "unknown"

	// Not `unknown`: build.date is a timestamp everywhere else, and a consumer
	// parsing it would have to special-case a word.
	zeroTime = "1970-01-01T00:00:00Z"
)

// Read assembles the four build attributes, reporting an absence as a value
// rather than an error: a binary with no VCS stamp is a normal thing to be, and
// a Resource that refused to build over it is a service that cannot boot in a
// test.
func Read() Stamp {
	stamp := linkerStamp()

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return stamp
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			stamp.Commit = setting.Value
		case "vcs.time":
			// The COMMIT's timestamp, not the moment the compiler ran — the
			// reproducible half, and the one that answers which change is
			// deployed. onix's onix.build.date is the other.
			stamp.Date = setting.Value
		case "vcs.modified":
			stamp.TreeState = treeStateFromVCS(setting.Value)
		}
	}

	return stamp
}

// linkerStamp is what -ldflags supplied, with the unknown values standing in
// wherever it supplied nothing.
//
// It is the floor rather than the answer: Read lets the VCS settings overwrite
// every field they cover, because the toolchain observed the tree it compiled
// and the build system only asserted something about it. The two agree on a
// clean checkout and disagree exactly where the observation is worth more — a
// tree edited after the build system read `git status`.
func linkerStamp() Stamp {
	stamp := Stamp{
		Version:   version,
		Commit:    unknownRevision,
		TreeState: unknownTreeState,
		Date:      zeroTime,
	}
	if commit != "" {
		stamp.Commit = commit
	}
	if buildDate != "" {
		stamp.Date = buildDate
	}
	if treeState != "" {
		stamp.TreeState = treeState
	}
	return stamp
}

// treeStateFromVCS maps debug.BuildSetting's "true"/"false" onto the registry's
// clean/dirty/unknown. Three values and not two, because `dirty` on a production
// Resource is a finding and must not be confusable with a missing stamp.
func treeStateFromVCS(modified string) string {
	switch modified {
	case "true":
		return "dirty"
	case "false":
		return "clean"
	default:
		return unknownTreeState
	}
}
