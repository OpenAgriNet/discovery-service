package architecture

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ldflagTarget matches the -X symbol path in either build file: everything
// between `-X ` and the `=` that starts its value.
var ldflagTarget = regexp.MustCompile(`-X ([^\s=]+)=`)

// buildFilesThatStamp are the two files that link the service binary. Both must
// name the same symbol; neither may be the only one that does.
var buildFilesThatStamp = []string{"Makefile", "Dockerfile"}

// releaseVersionOverride matches the workflow-level env entry that hands the
// triggering tag to the Makefile.
var releaseVersionOverride = regexp.MustCompile(`(?m)^\s*VERSION:\s*\$\{\{\s*github\.ref_name\s*\}\}\s*$`)

// makefileVersionIsOverridable matches `VERSION ?=` and nothing else. `:=` or a
// bare `=` in that position would make the assignment unconditional.
var makefileVersionIsOverridable = regexp.MustCompile(`(?m)^VERSION\s*\?=`)

// TestTheReleaseWorkflowNamesTheTagItWasStartedBy pins where a released image's
// version comes from.
//
// It must be github.ref_name — the tag whose push started the run — and never
// `git describe`, which reports whichever tag pointing at HEAD was CREATED last.
// Tag a commit v1.0.0, add v1.0.1-rc1 to the same commit later, push v1.0.0:
// describe says v1.0.1-rc1, the image ships under a name nobody released, and
// because that name carries a hyphen the :latest guard silently declines to move
// it. Nothing about the run looks wrong.
//
// Two things have to hold together and neither is visible from the other's file,
// which is why one test asserts both: the workflow has to SET the variable, and
// the Makefile's `?=` has to let it win. Change either alone and the release
// reverts to describe on a runner.
//
// The `zz-decoy` tag is the manual half of this fixture — an annotated tag
// created after v0.0.1-rc4 on the same commit, so a local `git describe` picks
// it and a local build stamps service.version=zz-decoy-N-g<sha>. That is the
// reproduction working, not a fault, and the Makefile says so at length. This
// test is the half that runs unattended: the tag reproduces the bug on demand,
// and this refuses the change that would let it back in.
func TestTheReleaseWorkflowNamesTheTagItWasStartedBy(t *testing.T) {
	workflow := filepath.Join(".github", "workflows", "ci-release.yml")

	body, err := os.ReadFile(filepath.Join(repoRoot, workflow))
	if err != nil {
		t.Fatalf("read %s: %v", workflow, err)
	}
	if !releaseVersionOverride.Match(body) {
		t.Errorf("%s sets no `VERSION: ${{ github.ref_name }}`.\n"+
			"        Without it the Makefile's `git describe` fallback runs on the runner, and a\n"+
			"        commit carrying two tags releases under whichever was created last.", workflow)
	}

	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if !makefileVersionIsOverridable.Match(makefile) {
		t.Errorf("the Makefile does not declare VERSION with `?=`.\n"+
			"        %s's env block would then be ignored and every release would take the\n"+
			"        `git describe` answer, which is the tag created last and not the one pushed.", workflow)
	}
}

// TestBothBuildFilesStampTheSameSymbol closes the one silent failure -ldflags
// has.
//
// `go build -X does/not/exist.version=1.2.3` succeeds. It prints nothing, exits
// zero and produces a binary in which the flag did nothing — so renaming
// src/platform/telemetry, or moving `version` out of it, leaves a green build,
// a green test suite and a production Resource reporting `dev` for every
// release after. The only thing that notices is somebody asking OP5's question
// months later and finding the answer has been wrong the whole time.
//
// cmd/discovery-service/main.go's writeBuildInfo avoided the problem entirely by
// refusing -ldflags. That is still the right default and three of the four build
// attributes still take it; service.version is the one that cannot, because
// debug.BuildInfo.Main.Version carries the module's version and never the
// release tag — a pseudo-version in a git checkout on go1.25, `(devel)` in the
// .git-less release image. This test is the price of the exception, and it is
// the encoded form of the pin rather than a comment asking the next person to
// remember.
func TestBothBuildFilesStampTheSameSymbol(t *testing.T) {
	targets := map[string]string{}

	for _, name := range buildFilesThatStamp {
		body, err := os.ReadFile(filepath.Join(repoRoot, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		matches := ldflagTarget.FindAllStringSubmatch(string(body), -1)
		if len(matches) == 0 {
			t.Errorf("%s links the binary but stamps no -X symbol; every image it builds "+
				"will report service.version=dev", name)
			continue
		}
		if len(matches) > 1 {
			t.Errorf("%s stamps %d -X symbols; this test assumes one and would silently "+
				"check only the first", name, len(matches))
			continue
		}
		targets[name] = matches[0][1]
	}

	if len(targets) != len(buildFilesThatStamp) {
		t.FailNow()
	}

	makefile, dockerfile := targets["Makefile"], targets["Dockerfile"]
	if makefile != dockerfile {
		t.Fatalf("the two build files stamp different symbols:\n"+
			"        Makefile:   %s\n"+
			"        Dockerfile: %s\n"+
			"        A local build and a released image would then report different versions.",
			makefile, dockerfile)
	}

	assertSymbolExists(t, makefile)
}

// assertSymbolExists resolves the -X target back to a declaration.
//
// Splitting on the last dot is what the linker does: everything before it is an
// import path and everything after is a package-level variable name. Checking
// the variable is declared as a string is the part that matters — -X silently
// declines to set anything else, so `var version int` would fail exactly as
// quietly as a missing symbol.
func assertSymbolExists(t *testing.T, target string) {
	t.Helper()

	dot := strings.LastIndex(target, ".")
	if dot < 0 {
		t.Fatalf("-X target %q is not <import path>.<variable>", target)
	}
	importPath, variable := target[:dot], target[dot+1:]

	if !strings.HasPrefix(importPath, modulePath+"/") {
		t.Fatalf("-X target %q is outside this module; the linker will not resolve it", target)
	}
	directory := filepath.Join(repoRoot, strings.TrimPrefix(importPath, modulePath+"/"))

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("-X target %q names package %s, which does not exist: %v", target, importPath, err)
	}

	declaration := regexp.MustCompile(`(?m)^var ` + regexp.QuoteMeta(variable) + ` = "`)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		if declaration.Match(body) {
			return
		}
	}
	t.Errorf("-X target %q names %s in %s, and no file there declares it as a string variable.\n"+
		"        go build accepts an -X naming nothing and silently does nothing, so this\n"+
		"        would ship every release stamped `dev`.", target, variable, importPath)
}
