package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The layered loader is exercised through load rather than Load so the process
// environment is a parameter. `make test` pins EMBEDDING_PROVIDER=hashing, so a
// test reading os.Environ would be asserting against the Makefile.
const (
	repoCommonYAML = "../../../config/common.yaml"
	noInstance     = "instance-that-does-not-exist.yaml"
)

// baseEnv supplies only the two values that have no default and no file:
// APP_NETWORK_ID has no repo-wide answer, and DATABASE_URL is a secret.
func baseEnv() map[string]string {
	return map[string]string{
		"APP_NETWORK_ID": "mahavistar",
		"DATABASE_URL":   "postgres://discovery@localhost:5432/discovery",
	}
}

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "layer.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestDefaultsAreTheFloor(t *testing.T) {
	cfg, err := Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}

	// Every value the plan states by name. A default that drifts silently
	// changes what the service does on a deployment that configures nothing.
	assertEqual(t, "App.DefaultTimezone", cfg.App.DefaultTimezone, "Asia/Kolkata")
	assertEqual(t, "Search.DefaultPageSize", cfg.Search.DefaultPageSize, 20)
	assertEqual(t, "Search.MaxRadiusMeters", cfg.Search.MaxRadiusMeters, 200000)
	assertEqual(t, "Search.MaxCandidatesPerMode", cfg.Search.MaxCandidatesPerMode, 500)
	assertEqual(t, "Search.FailOnUnavailableMode", cfg.Search.FailOnUnavailableMode, false)
	assertEqual(t, "Embeddings.Provider", cfg.Embeddings.Provider, "noop")
	assertEqual(t, "Embeddings.Dimensions", cfg.Embeddings.Dimensions, 768)
	assertEqual(t, "Validation.EnableL1Schema", cfg.Validation.EnableL1Schema, true)
	assertEqual(t, "Validation.EnableL2Context", cfg.Validation.EnableL2Context, true)
	assertEqual(t, "Auth.EnableSignatureVerification", cfg.Auth.EnableSignatureVerification, false)
	assertEqual(t, "OTel.Exporter", cfg.OTel.Exporter, "none")

	// The three knobs the tasks that read them do not get to define. Two are
	// security- or conformance-shaping and false is the safe end of both; the
	// third is a resolution the whole geo index is built at.
	assertEqual(t, "Errors.IncludeLegacyType", cfg.Errors.IncludeLegacyType, false)
	assertEqual(t, "Ext.AllowNetworkFetch", cfg.Ext.AllowNetworkFetch, false)
	assertEqual(t, "Geo.ResolutionCells", cfg.Geo.ResolutionCells, 8)
}

func TestEveryLayerBeatsTheOneBelowIt(t *testing.T) {
	floor, err := Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}

	common := writeYAML(t, "search:\n  maxPageSize: 60\nlog:\n  level: warn\n")
	instance := writeYAML(t, "search:\n  maxPageSize: 70\n")

	t.Run("common beats the floor", func(t *testing.T) {
		cfg, err := load(common, noInstance, baseEnv())
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if floor.Search.MaxPageSize == 60 {
			t.Fatal("fixture is not a change: the floor already reads 60")
		}
		assertEqual(t, "Search.MaxPageSize", cfg.Search.MaxPageSize, 60)
	})

	t.Run("instance beats common", func(t *testing.T) {
		cfg, err := load(common, instance, baseEnv())
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		assertEqual(t, "Search.MaxPageSize", cfg.Search.MaxPageSize, 70)
	})

	t.Run("the environment beats instance", func(t *testing.T) {
		environment := baseEnv()
		environment["SEARCH_MAX_PAGE_SIZE"] = "80"

		cfg, err := load(common, instance, environment)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		assertEqual(t, "Search.MaxPageSize", cfg.Search.MaxPageSize, 80)
	})
}

// The other direction: a layer must not clobber a value it says nothing about.
// env.Parse applies every envDefault tag it can, so a naive third pass would
// reset each field no environment variable names — which is the failure the
// precedence assertions above cannot see.
func TestALayerDoesNotClobberAValueItDoesNotSet(t *testing.T) {
	common := writeYAML(t, "search:\n  maxPageSize: 60\nlog:\n  level: warn\n")
	instance := writeYAML(t, "server:\n  port: 9090\n")

	environment := baseEnv()
	environment["SEARCH_DEFAULT_PAGE_SIZE"] = "25"

	cfg, err := load(common, instance, environment)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	assertEqual(t, "Log.Level (set by common only)", cfg.Log.Level, "warn")
	assertEqual(t, "Search.MaxPageSize (set by common only)", cfg.Search.MaxPageSize, 60)
	assertEqual(t, "Server.Port (set by instance only)", cfg.Server.Port, 9090)
	assertEqual(t, "Search.DefaultPageSize (set by env only)", cfg.Search.DefaultPageSize, 25)
	assertEqual(t, "App.DefaultTimezone (set by nothing)", cfg.App.DefaultTimezone, "Asia/Kolkata")
}

// A blank variable is not a value, at any layer. env.Parse reads a
// present-but-blank entry as absent and applies the envDefault tag, so copying
// one over the YAML layer erases a reviewed value and resurrects the tag
// default in its place — neither the file's answer nor the operator's. A
// `value: ""` in a pod spec and a blank key arriving through envFrom are the
// ordinary shapes this arrives in, and instance.yaml is the layer an operator
// is most likely to have set.
func TestABlankVariableDoesNotEraseTheLayerBelow(t *testing.T) {
	instance := writeYAML(t, "search:\n  maxPageSize: 40\nlog:\n  level: warn\nvalidation:\n  specURL: https://spec.example/beckn.yaml\n")

	environment := baseEnv()
	for _, blank := range []string{"SEARCH_MAX_PAGE_SIZE", "LOG_LEVEL", "VALIDATION_SPEC_URL"} {
		environment[blank] = ""
	}

	cfg, err := load(repoCommonYAML, instance, environment)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// 40 is neither the tag default nor common.yaml's value, so this
	// distinguishes the instance layer surviving from either one replacing it.
	assertEqual(t, "Search.MaxPageSize", cfg.Search.MaxPageSize, 40)
	assertEqual(t, "Log.Level", cfg.Log.Level, "warn")

	// The same rule where there is no tag to resurrect: a blank variable used
	// to win here and set the field empty, which made one spelling mean "clear
	// this" for the five fields with no default and "restore the default" for
	// the twenty with one. Uniform is the point — precedence is a property of
	// the layer, not of whether a field happens to carry a tag.
	assertEqual(t, "Validation.SpecURL", cfg.Validation.SpecURL, "https://spec.example/beckn.yaml")
}

// The same rule one layer down, and there a refusal rather than a skip: a blank
// in a reviewed file is a deliberate keystroke, so it must say so loudly rather
// than quietly erase the layer below and put the envDefault tag back in its
// place. One gesture with two opposite outcomes — ignored in the environment,
// destructive in a file — is the ambiguity the unknown-key refusal exists to
// prevent. Refusing costs only a spelling indistinguishable from omitting the
// key, which is how a layer says "take the one below" already.
func TestABlankValueInAFileFailsStartup(t *testing.T) {
	cases := map[string]struct{ blanked, names string }{
		// A field with a tag: "" would come back as info — neither the file's
		// warn nor empty.
		"a blank string over a value the layer below set": {
			"log:\n  level: \"\"\n", "log.level",
		},
		// A field without one: "" used to win outright and clear it.
		"a blank string where there is no tag to restore": {
			"validation:\n  specURL: \"\"\n", "validation.specURL",
		},
		// An empty sequence renders to the same empty string. It is the one
		// blank YAML can spell unambiguously, and its effect today is exactly
		// omitting the key — so it is refused for the same reason and at the
		// same cost, and the loader keeps one rule instead of two.
		"an empty sequence": {
			"replication:\n  targets: []\n", "replication.targets",
		},
	}

	common := writeYAML(t, "log:\n  level: warn\nvalidation:\n  specURL: https://spec.example/beckn.yaml\nreplication:\n  targets: [remote]\n")

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			instance := writeYAML(t, tc.blanked)

			if _, err := load(common, instance, baseEnv()); err == nil {
				t.Fatalf("load accepted %q", tc.blanked)
			} else if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("error %v does not name %q", err, tc.names)
			}

			// Refused in the reviewed file too: the rule is a property of the
			// layer, not of which of the two files it is written in.
			if _, err := load(instance, noInstance, baseEnv()); err == nil {
				t.Fatalf("load accepted %q in common.yaml", tc.blanked)
			}
		})
	}
}

func TestYAMLKeysMatchFieldsCaseInsensitively(t *testing.T) {
	common := writeYAML(t, "APP:\n  defaulttimezone: Europe/Berlin\n")

	cfg, err := load(common, noInstance, baseEnv())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	assertEqual(t, "App.DefaultTimezone", cfg.App.DefaultTimezone, "Europe/Berlin")
}

func TestAnUnknownYAMLKeyFailsStartup(t *testing.T) {
	cases := map[string]struct{ body, names string }{
		"unknown group":       {"seach:\n  maxPageSize: 60\n", "seach"},
		"unknown key":         {"search:\n  maxLimit: 60\n", "search.maxLimit"},
		"group given a value": {"search: 60\n", "search"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeYAML(t, tc.body)

			if _, err := load(path, noInstance, baseEnv()); err == nil {
				t.Fatalf("load accepted %q", tc.body)
			} else if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("error %v does not name %q", err, tc.names)
			}

			// The same key is a startup failure in either file, not only in
			// the reviewed one.
			if _, err := load(repoCommonYAML, path, baseEnv()); err == nil {
				t.Fatalf("load accepted %q in instance.yaml", tc.body)
			}
		})
	}
}

func TestAMissingInstanceFileIsNotAnError(t *testing.T) {
	if _, err := load(repoCommonYAML, noInstance, baseEnv()); err != nil {
		t.Fatalf("load: %v", err)
	}
}

// common.yaml is committed and copied into the image; instance.yaml is mounted.
// A deployment missing the reviewed defaults is broken, not under-configured.
func TestAMissingCommonFileIsAnError(t *testing.T) {
	if _, err := load("common-that-does-not-exist.yaml", noInstance, baseEnv()); err == nil {
		t.Fatal("load accepted a missing common.yaml")
	}
}

// The reviewed file is loaded by the same walk that rejects unknown keys, so a
// typo in it is a boot failure — and this is where it is caught before a boot.
func TestTheCommittedCommonYAMLLoads(t *testing.T) {
	cfg, err := load(repoCommonYAML, noInstance, baseEnv())
	if err != nil {
		t.Fatalf("load %s: %v", repoCommonYAML, err)
	}
	assertEqual(t, "Search.MaxRadiusMeters", cfg.Search.MaxRadiusMeters, 200000)
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]struct{ body, names string }{
		// A page bound below the default page size cannot clamp the value it
		// is asked to clamp.
		"a max page size below the default page size": {
			"search:\n  defaultPageSize: 50\n  maxPageSize: 20\n", "maxPageSize",
		},
		// The candidate pool is also the reachable pagination depth, so a pool
		// smaller than one page cannot fill even the first one.
		"a candidate pool smaller than one page": {
			"search:\n  maxPageSize: 100\n  maxCandidatesPerMode: 50\n", "maxCandidatesPerMode",
		},
		"an unloadable timezone": {
			"app:\n  defaultTimezone: Mars/Olympus_Mons\n", "defaultTimezone",
		},
		// H3 stops at 15. Caught here, this is one startup error; caught later
		// it is a failure inside the first cover a publish or discover builds.
		"an H3 resolution that does not exist": {
			"geo:\n  resolutionCells: 16\n", "resolutionCells",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeYAML(t, tc.body)

			_, err := load(path, noInstance, baseEnv())
			if err == nil {
				t.Fatalf("load accepted %q", tc.body)
			}
			if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("error %v does not name %q", err, tc.names)
			}
		})
	}
}

// Every problem in one boot: an operator fixing a bad file should not have to
// restart once per mistake.
func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	path := writeYAML(t, "app:\n  defaultTimezone: Mars/Olympus_Mons\nsearch:\n  defaultPageSize: 50\n  maxPageSize: 20\n")

	_, err := load(path, noInstance, baseEnv())
	if err == nil {
		t.Fatal("load accepted two invalid values")
	}
	for _, want := range []string{"defaultTimezone", "maxPageSize"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not name %q", err, want)
		}
	}
}

func TestTheTwoValuesWithNoDefaultAreRequired(t *testing.T) {
	for _, key := range []string{"APP_NETWORK_ID", "DATABASE_URL"} {
		t.Run(key, func(t *testing.T) {
			environment := baseEnv()
			delete(environment, key)

			if _, err := load(repoCommonYAML, noInstance, environment); err == nil {
				t.Fatalf("load accepted a configuration with no %s", key)
			}
		})
	}
}

// The YAML overlay reaches a field through its env tag, so a leaf without one
// is a key no file can set — silently, which is the failure mode this whole
// loader exists to refuse.
func TestEveryLeafFieldDeclaresAnEnvTag(t *testing.T) {
	var missing []string
	walkLeaves(reflect.TypeFor[Config](), "", func(path string, field reflect.StructField) {
		if _, ok := field.Tag.Lookup("env"); !ok {
			missing = append(missing, path)
		}
	})
	if len(missing) > 0 {
		t.Errorf("fields with no env tag: %s", strings.Join(missing, ", "))
	}
}

func walkLeaves(t reflect.Type, prefix string, visit func(string, reflect.StructField)) {
	for i := range t.NumField() {
		field := t.Field(i)
		if field.Type.Kind() == reflect.Struct {
			walkLeaves(field.Type, prefix+field.Name+".", visit)
			continue
		}
		visit(prefix+field.Name, field)
	}
}

func assertEqual[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
