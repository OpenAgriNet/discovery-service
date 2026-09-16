package architecture

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// A citation to a file in this repository must name a symbol, not a line.
//
// The repository is dense with cross-references — a Go comment pointing at the
// paragraph of `opentelemetry.md` that decided it, a design document pointing
// at the function that implements it. They are the reason the "why" survives.
// Written as `file.go:207` they also rot silently: the next edit above line 207
// moves the target and nothing anywhere fails, so the citation now points at an
// unrelated line and reads as authoritative. That is worse than no citation,
// because a reader who follows it and finds a closing brace concludes the
// document is stale rather than that the pin is.
//
// The 2026-09-09 sweep found this had already happened at scale. `setStatus`
// was pinned to lines 202-207 of `middlewares/trace.go` in three files when the
// function had moved to 160 and the file was only 184 lines long. Four more
// citations pinned lines 158-163 of `router.go` while describing `probes`,
// which is at 142 — those lines are the `healthz` doc comment, so all four read
// as authoritative and all four were wrong. Eight pins named a line past the
// end of the file. None of it failed anything.
//
// A symbol does not have this failure mode. `middlewares/trace.go`'s
// `setStatus` survives every edit that does not rename or delete it, and a
// rename that leaves the citation behind is a grep away rather than invisible.
// So the rule is: cite the identifier for Go, the `§Heading` for Markdown.
//
// Line numbers into ANOTHER repository or a pinned module version are fine and
// are what externalFiles below exists to permit — we cannot grep beckn-onix
// from here, the citation names a revision that does not move, and there is
// nothing to keep in step.
//
// Fenced blocks in Markdown are skipped, because what is in them is output, not
// prose. `docs/telemetry.md`'s Logs section holds a log line captured from a
// running stack, and zap's own `caller` field in it names a line in
// `middlewares/request_logger.go`. Editing that to name a symbol would make
// the transcript a thing the logger never emitted — the file's whole claim is
// that every payload in it came off a real stack. The same applied to the
// fabricated `--- FAIL:` blocks in `telemetry-seam.md` before that document was
// drained into `opentelemetry.md` on 2026-09-09. The cost is a blind spot: a
// citation written inside a fence is not checked, so put the ones that matter in
// prose, where a reader meets them anyway.

// citation matches `some/path/file.ext:123` and `file.ext:12-34` / `file.ext:12,34`.
var citation = regexp.MustCompile(`([A-Za-z0-9_./-]+\.(?:md|go|yaml|json|txt)):(\d+)(?:[-,]\d+)?`)

// externalFiles are basenames that name a file in a DIFFERENT repository or in
// a third-party module. A line pin into one of these is legitimate: it cites a
// fixed upstream revision that this repository does not edit and cannot check.
//
// This list is deliberately a list of basenames rather than a wildcard. A new
// external dependency has to be added here by hand, which is the moment someone
// states which repository it lives in — and that statement is the only record
// this repository has of where these files are.
var externalFiles = map[string]string{
	// github.com/sunbird-obsrv network-telemetry-spec, at docs/.
	"otel-specification.md": "network-telemetry-spec",

	// github.com/beckn/beckn-onix — the reference implementation the telemetry
	// design is checked against. docs/design/telemetry-examples/adapter.json
	// records what it was observed to emit, cited to these files.
	"stdHandler.go":        "beckn-onix",
	"otelsetup.go":         "beckn-onix",
	"http_metric.go":       "beckn-onix",
	"pluginMetrics.go":     "beckn-onix",
	"step.go":              "beckn-onix",
	"step_instrumentor.go": "beckn-onix",
	"upstream.go":          "beckn-onix",
	"sunbirdRegistry.go":   "beckn-onix",
	"errors.go":            "beckn-onix",
	"OBSERVABILITY.md":     "beckn-onix",
	"config.yaml":          "beckn-onix (its collector configs)",

	// Third-party Go modules, cited at the version go.mod pins.
	"env.go": "github.com/caarlos0/env",
}

// skipDirs are trees whose contents are not ours to reformat.
var skipDirs = []string{".git", "bin", "node_modules", ".cache", "vendor"}

// scannedExtensions are the text files that carry prose worth citing from.
var scannedExtensions = []string{".go", ".md", ".yaml", ".yml", ".json"}

// generatedFiles are written by a tool, so a citation in one is a copy of a
// citation in the source the tool read — fixing it there fixes it here.
var generatedFiles = []string{
	"tests/testdata/beckn-v2.0.0.yaml", // the upstream Beckn spec, verbatim
	"docs/api/openapi.yaml",
	"tests/covertool/testdata", // synthetic coverage fixtures naming example.com
}

func TestNoCitationPinsALineInThisRepository(t *testing.T) {
	ours := repositoryBasenames(t)
	var offences []string
	scanned := 0

	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		if entry.IsDir() {
			if slices.Contains(skipDirs, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !slices.Contains(scannedExtensions, filepath.Ext(path)) {
			return nil
		}
		if slices.ContainsFunc(generatedFiles, func(g string) bool {
			return strings.HasPrefix(filepath.ToSlash(relative), g)
		}) {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		markdown := filepath.Ext(path) == ".md"
		fenced := false
		for number, line := range strings.Split(string(body), "\n") {
			if markdown && strings.HasPrefix(strings.TrimSpace(line), "```") {
				fenced = !fenced
				continue
			}
			if fenced {
				continue
			}
			for _, match := range citation.FindAllStringSubmatch(line, -1) {
				whole, target := match[0], match[1]
				if !citesThisRepository(target, ours) {
					continue
				}
				offences = append(offences, filepath.ToSlash(relative)+":"+
					itoa(number+1)+" cites "+whole)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", repoRoot, err)
	}
	if scanned == 0 {
		t.Fatal("scanned no files, so this test asserts nothing")
	}

	if len(offences) > 0 {
		t.Errorf("%d citation(s) pin a line number in this repository. Cite the "+
			"symbol instead — the identifier for Go, the §Heading for Markdown — "+
			"because a line moves and nothing fails when it does:\n\t%s",
			len(offences), strings.Join(offences, "\n\t"))
	}
}

// TestEveryExternalFileIsStillCited keeps externalFiles from becoming a blanket
// exemption. An entry nobody cites is one that was either renamed upstream or
// dropped, and either way the next person to add a same-named file to THIS
// repository would find their line pins silently permitted.
func TestEveryExternalFileIsStillCited(t *testing.T) {
	cited := map[string]bool{}

	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if slices.Contains(skipDirs, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !slices.Contains(scannedExtensions, filepath.Ext(path)) {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range citation.FindAllStringSubmatch(string(body), -1) {
			cited[filepath.Base(match[1])] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", repoRoot, err)
	}

	for name, repository := range externalFiles {
		if !cited[name] {
			t.Errorf("externalFiles permits line pins into %q (%s) and nothing "+
				"cites it any more. Delete the entry: while it is here, a file of "+
				"that name added to this repository gets the exemption too",
				name, repository)
		}
	}
}

// pinnedModuleVersion matches a `v1.44.0/` path element — a path carrying a
// module version names a dependency at the revision go.mod pins, never our
// tree. Anchored at either a slash or the start, because both spellings appear.
var pinnedModuleVersion = regexp.MustCompile(`(^|/)v\d+\.\d+\.\d+/`)

// citesThisRepository reports whether target names a file this repository owns.
// Basename matching against a pre-built index, because most citations are
// written relative to the reader rather than to the root — a bare file name
// far more often than a path from the module root.
func citesThisRepository(target string, ours map[string]bool) bool {
	base := filepath.Base(target)
	if _, external := externalFiles[base]; external {
		return false
	}
	if pinnedModuleVersion.MatchString(target) || strings.HasPrefix(target, "example.com/") {
		return false
	}
	return ours[base]
}

// repositoryBasenames indexes every file in the tree by basename, once.
func repositoryBasenames(t *testing.T) map[string]bool {
	t.Helper()
	index := map[string]bool{}
	err := filepath.WalkDir(repoRoot, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if slices.Contains(skipDirs, entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		index[entry.Name()] = true
		return nil
	})
	if err != nil {
		t.Fatalf("indexing %s: %v", repoRoot, err)
	}
	return index
}

// itoa avoids pulling strconv in for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for ; n > 0; n /= 10 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
	}
	return string(digits)
}
