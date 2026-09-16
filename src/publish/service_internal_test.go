package publish

import (
	"testing"

	"github.com/OpenAgriNet/discovery-service/src/domain"
)

// rebase's own fallback — a fault whose Path is unreadable still names the
// catalog it came from, when the caller has one to name. requestRelative
// (geometry-walk faults) always passes -1, so this branch is only reached by
// rebase's OTHER caller inside publishOne, and only for a fault whose Path
// jsonpath.Dot cannot render — worth pinning directly, since it is rebase's
// own documented fallback rather than a defensive dead end.
//
// package publish, not publish_test: rebase is unexported.
func TestRebaseFallsBackToTheCatalogWhenTheFaultsPathIsUnreadable(t *testing.T) {
	faults := []domain.Fault{{Path: "", Code: "X", Message: "unreadable"}}

	errors := rebase(faults, func(dotted string) string { return dotted }, 2)
	if len(errors) != 1 {
		t.Fatalf("errors = %+v, want exactly one", errors)
	}
	if errors[0].Details == nil || errors[0].Details.Path != catalogPath(2) {
		t.Errorf("Details.Path = %+v, want %q", errors[0].Details, catalogPath(2))
	}
}

// directivePath and resourceDirectivePath both fall back to naming the
// catalog when a catalog was published with no directive of its own
// (directiveIndex < 0) — documented on directivePath, but unreachable through
// intakeRefusal's two callers: both only fire when the directive that was
// found carries MASTER or an Extends, which by construction means a
// directive WAS found (directiveIndex >= 0). Pinned directly against the
// contract rather than left for a caller that cannot exercise it.
func TestDirectivePathFallsBackToTheCatalogWithNoDirectiveOfItsOwn(t *testing.T) {
	req := request{catalogIndex: 3, directiveIndex: -1}

	if got, want := req.directivePath(), catalogPath(3); got != want {
		t.Errorf("directivePath() = %q, want %q", got, want)
	}
	if got, want := req.resourceDirectivePath(0), catalogPath(3); got != want {
		t.Errorf("resourceDirectivePath(0) = %q, want %q", got, want)
	}
}
