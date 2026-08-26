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
	assertEqual(t, "Validation.EnableL2Context", cfg.Validation.EnableL2Context, false)
	assertEqual(t, "Auth.EnableSignatureVerification", cfg.Auth.EnableSignatureVerification, false)
	assertEqual(t, "OTel.Exporter", cfg.OTel.Exporter, "none")

	// C14's ceiling. It is the only bound on what an unauthenticated caller can
	// make this process allocate, so a drift here is a drift in the service's
	// exposure and not merely in a number.
	assertEqual(t, "Server.MaxRequestBodyBytes", cfg.Server.MaxRequestBodyBytes, int64(10485760))

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

// An explicit empty sequence is not a blank, and is the one exception to the
// rule above. `key: ""` is ambiguous — it reads as both "no value" and "the
// empty string" — and earns the refusal. `[]` has one meaning in YAML, and for
// Replication.Targets empty is a value rather than an absence: it is how the
// no-op replicator is selected. Refusing it would leave a deployment no way to
// say it replicates to nothing once the reviewed layer names a target, because
// no other layer can clear one either.
func TestAnExplicitEmptyListClearsTheLayerBelow(t *testing.T) {
	common := writeYAML(t, "replication:\n  targets: [remote-a, remote-b]\n")
	instance := writeYAML(t, "replication:\n  targets: []\n")

	cfg, err := load(common, instance, baseEnv())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Replication.Targets) != 0 {
		t.Errorf("Replication.Targets = %v, want empty", cfg.Replication.Targets)
	}

	// The fixture is a change and not a coincidence: without the instance layer
	// the two targets stand, so the assertion above is the clearing and not the
	// absence of a value to clear.
	below, err := load(common, noInstance, baseEnv())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	assertEqual(t, "Replication.Targets below the instance layer", len(below.Replication.Targets), 2)
}

// The clearing spelling is a property of the field, not of the value's shape. A
// list where the field takes a value is a mistake in either direction: an empty
// one would resolve to a blank and so erase the layer below exactly as `key: ""`
// would, which is the failure the refusal above exists to prevent, and a
// populated one would be joined into a string the field never asked for. Nested,
// an empty list would become an empty element inside a slice — junk that reaches
// the field, where the same position spelled "" is refused.
func TestOnlyASliceFieldTakesAList(t *testing.T) {
	cases := map[string]struct{ body, names string }{
		"an empty list where the field takes a value": {
			"log:\n  level: []\n", "log.level",
		},
		"a populated list where the field takes a value": {
			"log:\n  level: [warn, info]\n", "log.level",
		},
		"an empty list nested inside a list": {
			"replication:\n  targets: [remote-a, []]\n", "replication.targets",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeYAML(t, tc.body)

			if _, err := load(path, noInstance, baseEnv()); err == nil {
				t.Fatalf("load accepted %q", tc.body)
			} else if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("error %v does not name %q", err, tc.names)
			}
		})
	}
}

// Clearing works because env.Parse never sets a blank value (env.go:507), so the
// field keeps its zero value. That holds only where there is no envDefault to
// come back in its place: a slice field carrying one would answer the clearing
// spelling above with the tag instead, silently. No slice field has a default
// today, and this is what stops one arriving without the trap being noticed.
func TestNoSliceFieldCarriesADefault(t *testing.T) {
	var offenders []string
	walkLeaves(reflect.TypeFor[Config](), "", func(path string, field reflect.StructField) {
		if field.Type.Kind() != reflect.Slice {
			return
		}
		if _, ok := field.Tag.Lookup("envDefault"); ok {
			offenders = append(offenders, path)
		}
	})
	if len(offenders) > 0 {
		t.Errorf("slice fields with an envDefault, which an empty list cannot clear: %s",
			strings.Join(offenders, ", "))
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
		// Zero reads as "unlimited" and means "refuse everything" — the two
		// worst possible readings of one value, so neither is reachable.
		"a body ceiling of zero": {
			"server:\n  maxRequestBodyBytes: 0\n", "maxRequestBodyBytes",
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

// Scenario 7. The flag has nothing behind it: Task 6's Ed25519 primitives are
// parked and the Signature middleware is not written, so `true` must refuse the
// boot. What made the deferral honest was never the flag but the impossibility
// of believing it was on when it was not — an operator who reads a security
// control back as enabled while every request goes unverified is the one
// failure mode worse than having no flag at all.
func TestSignatureVerificationRefusesToBoot(t *testing.T) {
	environment := baseEnv()
	environment["AUTH_ENABLE_SIGNATURE_VERIFICATION"] = "true"

	_, err := load(repoCommonYAML, noInstance, environment)
	if err == nil {
		t.Fatal("load accepted AUTH_ENABLE_SIGNATURE_VERIFICATION=true with nothing behind the flag")
	}
	if !strings.Contains(err.Error(), "AUTH_ENABLE_SIGNATURE_VERIFICATION") {
		t.Errorf("error %v does not name the flag an operator has to unset", err)
	}
}

// The other side of the same flag, which is the only supported Phase 1 setting:
// false is not merely accepted, it is the value the reviewed file ships.
func TestSignatureVerificationOffBoots(t *testing.T) {
	cfg, err := load(repoCommonYAML, noInstance, baseEnv())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Auth.EnableSignatureVerification {
		t.Error("auth.enableSignatureVerification is true with no environment setting it")
	}
}

// Task 10 was skipped by decision on 2026-08-26, so nothing sits behind
// VALIDATION_ENABLE_L2_CONTEXT: SchemaSource, the refresh loop, the L2
// validator and the schemas/<TypeName>/attributes.yaml set are all unbuilt.
// That makes it the twin of AUTH_ENABLE_SIGNATURE_VERIFICATION and it gets the
// twin's treatment — an operator reading a validation layer back as enabled,
// while every @context and @type goes unchecked, is worse off than one who can
// see there is no layer at all.
func TestL2ContextValidationRefusesToBoot(t *testing.T) {
	environment := baseEnv()
	environment["VALIDATION_ENABLE_L2_CONTEXT"] = "true"

	_, err := load(repoCommonYAML, noInstance, environment)
	if err == nil {
		t.Fatal("load accepted VALIDATION_ENABLE_L2_CONTEXT=true with nothing behind the flag")
	}
	if !strings.Contains(err.Error(), "VALIDATION_ENABLE_L2_CONTEXT") {
		t.Errorf("error %v does not name the flag an operator has to unset", err)
	}
}

// Defaulting it off is half the obligation and refusing `true` is the other
// half: refusing alone, over a tag that still reads `true`, would mean nothing
// boots at all.
func TestL2ContextValidationIsOffInTheReviewedFile(t *testing.T) {
	cfg, err := load(repoCommonYAML, noInstance, baseEnv())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Validation.EnableL2Context {
		t.Error("validation.enableL2Context is true with no environment setting it")
	}
	if !cfg.Validation.EnableL1Schema {
		t.Error("validation.enableL1Schema is false; L1 is built and is the layer that ships")
	}
}
