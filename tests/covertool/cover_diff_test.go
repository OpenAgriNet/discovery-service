// Package covertool tests the awk programs behind the coverage gate.
//
// They are gate logic — `make cover-diff` decides whether a PR is allowed to
// merge — so "nothing exercises the arithmetic" is not a gap that stays
// harmless. It did not: under -coverpkg the profile repeats every block once
// per test binary, the gate summed per line instead of per block, and a file
// with 81% real coverage reported 7%. Every PR touching a .go file failed.
// These fixtures pin the deduplication that fixes it.
package covertool

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The fixture repeats each block three times, the way -coverpkg does across
// test binaries, and hits each covered block in exactly ONE of the copies:
//
//	a.go  block 1 (10 stmts) hit in copy 2 only -> covered
//	a.go  block 2 (10 stmts) never hit          -> uncovered   => a.go  50%
//	b.go  block 1 ( 5 stmts) hit in copy 3 only -> covered     => b.go 100%
//
// so the aggregate is 15 of 20 statements, 60%. Summing per line instead of
// per block gives a.go 16%, b.go 33% and a total of 20% — which is the shape
// of the bug, not a rounding difference.
const (
	module     = "example.com/svc/"
	minPercent = "80"
)

func runCoverDiff(t *testing.T, profile string, changed ...string) string {
	t.Helper()

	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("awk not on PATH")
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	script := filepath.Join(filepath.Dir(thisFile), "..", "..", "tools", "cover-diff.awk")

	cmd := exec.Command("awk",
		"-v", "mod="+module,
		"-v", "min="+minPercent,
		"-f", script,
		"-", filepath.Join("testdata", profile),
	)
	cmd.Stdin = strings.NewReader(strings.Join(changed, "\n") + "\n")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("awk failed: %v\n%s", err, out)
	}
	return string(out)
}

// parse turns the program's output into the total and the set of files it
// reported as below the minimum.
func parse(t *testing.T, out string) (total string, below map[string]string) {
	t.Helper()
	below = map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Split(line, "\t")
		switch fields[0] {
		case "TOTAL":
			total = fields[1]
		case "FILE":
			below[fields[2]] = fields[1]
		case "EMPTY":
			total = "EMPTY"
		}
	}
	return total, below
}

// A block hit by any one test binary is covered, however many binaries missed
// it. This is the whole bug: the profile's repetition must not become a
// bigger denominator.
func TestDuplicatedBlocksCountOnce(t *testing.T) {
	out := runCoverDiff(t, "duplicated.out", "a.go", "b.go")
	total, below := parse(t, out)

	if total != "60" {
		t.Errorf("total = %s%%, want 60%% — a block hit in one of three copies is covered, not one-third covered\n%s", total, out)
	}

	if got, ok := below["b.go"]; ok {
		t.Errorf("b.go reported at %s%%, but every one of its statements is hit in some copy — it is 100%% and must not appear below the minimum\n%s", got, out)
	}

	if got := below["a.go"]; got != "50" {
		t.Errorf("a.go = %s%%, want 50%% (one covered block of two, equal size)\n%s", got, out)
	}
}

// Only the files on stdin are measured; the rest of the profile is not the
// PR's to answer for.
func TestUnchangedFilesAreIgnored(t *testing.T) {
	out := runCoverDiff(t, "duplicated.out", "b.go")
	total, below := parse(t, out)

	if total != "100" {
		t.Errorf("total = %s%%, want 100%% — only b.go was named\n%s", total, out)
	}
	if len(below) != 0 {
		t.Errorf("nothing should be below the minimum, got %v\n%s", below, out)
	}
}

// A changed file with no coverable statements is not 0% — it is unmeasurable,
// and the gate reports that rather than failing the PR.
func TestNoCoverableStatementsIsEmpty(t *testing.T) {
	out := runCoverDiff(t, "nostatements.out", "a.go")
	if total, _ := parse(t, out); total != "EMPTY" {
		t.Errorf("total = %q, want EMPTY\n%s", total, out)
	}
}

// A file in the diff that the profile never mentions contributes nothing at
// all, rather than dividing by zero.
func TestFileAbsentFromProfileIsEmpty(t *testing.T) {
	out := runCoverDiff(t, "duplicated.out", "never-compiled.go")
	if total, _ := parse(t, out); total != "EMPTY" {
		t.Errorf("total = %q, want EMPTY\n%s", total, out)
	}
}
