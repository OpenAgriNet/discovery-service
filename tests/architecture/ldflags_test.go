package architecture

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// ldflagTarget matches the -X symbol path in either build file: everything
// between `-X ` and the `=` that starts its value.
var ldflagTarget = regexp.MustCompile(`-X ([^\s=]+)=`)

// buildFilesThatStamp are the two files that link the service binary. Both must
// name the same symbols; neither may be the only one that does.
var buildFilesThatStamp = []string{"Makefile", "Dockerfile"}

// stampedSymbols are the four the Resource is assembled from. All four are
// linker-set because three of them USED to come from the toolchain's VCS stamp
// and silently did not in the one build that matters — see
// TestBothBuildFilesStampTheSameSymbols.
var stampedSymbols = []string{"version", "commit", "buildDate", "treeState"}

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

// TestBothBuildFilesStampTheSameSymbols closes the one silent failure -ldflags
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
// refusing -ldflags. That was the default for three of the four build
// attributes, and it was wrong for all three. debug.ReadBuildInfo's vcs.*
// settings are written only when the toolchain can see a git working tree, and
// the release image is built from a copied context that has none — so
// build.commit read `unknown`, build.tree_state read `unknown` and build.date
// read the epoch on EVERY image, which was confirmed against a running stack on
// 2026-09-09. A build stamp that is absent exactly where the binary is not the
// one you built is no stamp at all, so all four now cross as -X and the VCS
// settings only override them where they exist.
//
// service.version could never take the toolchain's answer either, for a
// different reason: debug.BuildInfo.Main.Version carries the module's version
// and never the release tag — a pseudo-version in a git checkout on go1.25,
// `(devel)` in the .git-less release image.
//
// This test is the price of all four exceptions, and it is the encoded form of
// the pin rather than a comment asking the next person to remember.
func TestBothBuildFilesStampTheSameSymbols(t *testing.T) {
	stamped := map[string][]string{}

	for _, name := range buildFilesThatStamp {
		body, err := os.ReadFile(filepath.Join(repoRoot, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var targets []string
		for _, match := range ldflagTarget.FindAllStringSubmatch(string(body), -1) {
			targets = append(targets, match[1])
		}
		if len(targets) == 0 {
			t.Errorf("%s links the binary but stamps no -X symbol; every image it builds "+
				"will report service.version=dev and an unknown commit", name)
			continue
		}
		slices.Sort(targets)
		stamped[name] = slices.Compact(targets)
	}

	if len(stamped) != len(buildFilesThatStamp) {
		t.FailNow()
	}

	makefile, dockerfile := stamped["Makefile"], stamped["Dockerfile"]
	if !slices.Equal(makefile, dockerfile) {
		t.Fatalf("the two build files stamp different symbols:\n"+
			"        Makefile:   %s\n"+
			"        Dockerfile: %s\n"+
			"        A local build and a released image would then describe themselves "+
			"differently, which is the one question a build stamp exists to answer.",
			strings.Join(makefile, ", "), strings.Join(dockerfile, ", "))
	}

	want := make([]string, 0, len(stampedSymbols))
	for _, symbol := range stampedSymbols {
		want = append(want, telemetryPackage+"."+symbol)
	}
	slices.Sort(want)
	if !slices.Equal(makefile, want) {
		t.Errorf("the build files stamp the wrong set of symbols:\n"+
			"        want: %s\n"+
			"        got:  %s\n"+
			"        Every attribute on the build Resource has to cross as -X. The three that\n"+
			"        did not — commit, date, tree_state — came from the toolchain's VCS stamp,\n"+
			"        which the release image's .git-less build context does not produce.",
			strings.Join(want, ", "), strings.Join(makefile, ", "))
	}

	for _, target := range makefile {
		assertSymbolExists(t, target)
	}
}

// telemetryPackage is where all four stamped symbols are declared.
var telemetryPackage = modulePath + "/src/platform/telemetry"

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
