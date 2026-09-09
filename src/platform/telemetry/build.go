package telemetry

import "runtime/debug"

// version is the only value in this service injected at link time:
//
//	-ldflags "-X github.com/OpenAgriNet/discovery-service/src/platform/telemetry.version=$(VERSION)"
//
// cmd/discovery-service/main.go's writeBuildInfo states the standing preference —
// read the toolchain's own build stamp rather than inject with -ldflags, so
// Makefile, Dockerfile and CI do not have to agree on a flag string. That
// preference holds for the other three attributes below and they take it. It
// cannot hold for this one, because debug.BuildInfo.Main.Version carries the
// MODULE's version and never the release tag the Makefile computes: in a git
// checkout on go1.25 it reads a pseudo-version, and in the release image, whose
// build stage copies no .git, it reads `(devel)`. Neither is the value OP5 wants
// on the Resource. So the exception is exactly one flag wide, which is the
// smallest thing three build systems can be asked to agree on.
//
// The three below take the stamp and inherit its gap rather than escaping it. In
// that same image they read `unknown`, `unknown` and the zero instant, for the
// same missing .git — so on a deployed binary this Resource answers OP5 from the
// flag alone and the VCS trio says nothing. Flagged rather than fixed: closing
// it means passing commit and date as build args too, which is the coordination
// cost the preference above exists to avoid, and that is a call to make on
// purpose.
//
// `dev` rather than "" because an empty Resource attribute is indistinguishable
// from an unset one: a facilitator seeing nothing cannot tell whether the
// participant declined to answer or the attribute was dropped in transit.
var version = "dev"

// Build is what -ldflags and the toolchain's VCS stamp know between them about
// the binary that is running.
type Build struct {
	Version   string
	Commit    string
	TreeState string
	Date      string
}

// The values the three VCS-derived attributes take when the binary carries no
// stamp — which is every `go test` binary and every build from an exported
// tree. Named because `unknown` appearing as a bare literal twice invites
// someone to make one of them "".
const (
	unknownRevision  = "unknown"
	unknownTreeState = "unknown"
)

// readBuild assembles the four build attributes.
//
// It reports an absence as a value rather than as an error, the same choice
// main.go's vcsRevision already made and for the same reason: a binary with no
// VCS stamp is a normal thing to be, and a Resource that cannot be built
// because of it is a service that cannot boot in a test.
func readBuild() Build {
	build := Build{
		Version:   version,
		Commit:    unknownRevision,
		TreeState: unknownTreeState,
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return build
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			build.Commit = setting.Value
		case "vcs.time":
			// The COMMIT's timestamp, not the moment the compiler ran. onix's
			// onix.build.date is the latter; this is the reproducible half and
			// the one that answers which change is deployed.
			build.Date = setting.Value
		case "vcs.modified":
			build.TreeState = treeState(setting.Value)
		}
	}

	if build.Date == "" {
		// Not `unknown`: this one is a timestamp everywhere else, and a
		// consumer parsing it would have to special-case a word. The zero
		// instant is unambiguous and parses.
		build.Date = zeroTime
	}
	return build
}

// zeroTime is what build.date says when the binary carries no VCS stamp.
const zeroTime = "1970-01-01T00:00:00Z"

// treeState maps the toolchain's spelling onto the registry's.
//
// debug.BuildSetting gives "true" or "false"; the registry declares
// clean/dirty/unknown. The translation is here rather than at the call site
// because a Resource reporting `false` for a clean tree would satisfy every
// other assertion about it, and `dirty` on a production Resource is a finding
// that must not be confusable with a missing stamp.
func treeState(modified string) string {
	switch modified {
	case "true":
		return "dirty"
	case "false":
		return "clean"
	default:
		return unknownTreeState
	}
}
