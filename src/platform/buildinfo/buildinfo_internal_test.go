package buildinfo

import "testing"

// TestAnUnstampedBinaryReportsTheFloor pins what a build with neither -ldflags
// nor a VCS stamp says about itself, which is every `go test` binary — so this
// test IS the case it describes and needs no fixture to be one.
//
// Each value answers something rather than being empty, and that is the point:
// an absent attribute and a declined one are indistinguishable to a facilitator
// reading the Resource, so the unstamped case has to say something a reader can
// recognise as "no stamp" instead of saying nothing.
func TestAnUnstampedBinaryReportsTheFloor(t *testing.T) {
	stamp := Read()

	for _, field := range []struct{ name, got, want string }{
		{"Version", stamp.Version, "dev"},
		{"Commit", stamp.Commit, unknownRevision},
		{"TreeState", stamp.TreeState, unknownTreeState},
		{"Date", stamp.Date, zeroTime},
	} {
		if field.got != field.want {
			t.Errorf("unstamped %s = %q, want %q", field.name, field.got, field.want)
		}
	}
}

// TestTheLinkerStampIsOnlyAFloor is the half of Read that a test binary cannot
// exercise: the linker values are compiled in, so the only way to vary them is
// to call linkerStamp with them set.
//
// It asserts the SUBSTITUTION rule — a supplied value replaces the unknown, an
// empty one does not. An earlier shape of this code assigned unconditionally,
// which turned "the build system supplied nothing" into an empty commit rather
// than `unknown`, and an empty string is exactly the value a reader cannot
// distinguish from a dropped attribute.
func TestTheLinkerStampIsOnlyAFloor(t *testing.T) {
	original := [4]string{version, commit, buildDate, treeState}
	t.Cleanup(func() {
		version, commit, buildDate, treeState = original[0], original[1], original[2], original[3]
	})

	version, commit, buildDate, treeState = "v1.2.3", "abc123", "2026-09-10T00:00:00Z", "clean"
	if got := linkerStamp(); got != (Stamp{"v1.2.3", "abc123", "clean", "2026-09-10T00:00:00Z"}) {
		t.Errorf("a fully stamped build reported %+v", got)
	}

	commit, buildDate, treeState = "", "", ""
	got := linkerStamp()
	if got.Version != "v1.2.3" {
		t.Errorf("Version = %q; -ldflags supplied it and nothing should overwrite it", got.Version)
	}
	if got.Commit != unknownRevision || got.TreeState != unknownTreeState || got.Date != zeroTime {
		t.Errorf("a partly stamped build reported %+v; the three unsupplied fields must read "+
			"as unknown rather than empty", got)
	}
}

// TestTreeStateFromVCSHasThreeStates, not two. `dirty` on a production Resource
// is a finding — a binary built from edits nobody can retrieve — and collapsing
// an unrecognised value onto `clean` would report that finding as its opposite.
func TestTreeStateFromVCSHasThreeStates(t *testing.T) {
	for modified, want := range map[string]string{
		"true":  "dirty",
		"false": "clean",
		"":      unknownTreeState,
		"yes":   unknownTreeState,
	} {
		if got := treeStateFromVCS(modified); got != want {
			t.Errorf("treeStateFromVCS(%q) = %q, want %q", modified, got, want)
		}
	}
}
