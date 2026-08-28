# Doc checkers

Run from `docs/registry`. Four need `jsonschema` (`packs.py` also needs `pyyaml`);
`links.py` needs nothing:

```
python3 verify/shape.py         # every record shown in any page here is a real record
python3 verify/records.py       # the records in examples.md, plus the six rules
python3 verify/auth_cases.py    # the auth matrix and the material grammar
python3 verify/links.py         # every `](file.md#anchor)` in this folder resolves
python3 verify/packs.py         # every resource shown here satisfies its network-specs domain pack
```

All five exit non-zero on failure, so they drop into CI unchanged.

`shape.py` scans every `.md` here except `README.md`, and treats a `json` block as a record claim
when its single top-level key is an entity name — so a page can show Beckn payloads and upstream
responses freely without tripping it. Each record must validate, and — **unioned across an
entity's blocks** — every declared property must be exercised. The union matters:
`auth.paramName` and `auth.paramNames` are mutually exclusive, so no single record can cover
both, and a per-block rule would force the docs to document one and stop documenting the other.
`examples.md` carries two probe records under *Forms no seeded record uses* to keep `paramNames`
and `valuePrefix` exercised, since no v1 record uses either.

`records.py` catches what a schema cannot. Validating the records is the easy half; the half that
matters is the [six rules](../schemas.md#six-rules-the-schema-cannot-express) — `bindingKey`
against its own two fields, references that resolve to live records, `paramNames` against
`secrets`, `version` against `schemaUrl`, `keyId` uniqueness within `node.keys`. Each is a record
that passes every pattern in its schema and still produces a failed call, or a silently
unverifiable signature, weeks later.

`paramNames` and `keyId` uniqueness are not violated by any seeded record, so they were verified
by mutation: inject the violation into a copy of `examples.md` and confirm the checker reports
that rule and not another.

`records.py` also carries one invariant that is not one of the five: **no secret in `examples.md`
may be anything but `env://`.** The schema permits `inline:` on purpose — an operator who cannot
set an environment variable needs it — but a reviewed file in git is never that operator, and the
day someone pastes a working key into an example is the day it is in every clone.
Mutation-tested.

`auth_cases.py` carries negative cases as well as positive ones. A case that passes for the wrong
reason is worse than no case — before `paramNames` existed, every `paramNames` negative passed
because `additionalProperties: false` refused the field outright. They are kept so the same cases
now fail for the intended reason.

Its `NARROWING` block pins the material grammar in both directions: a **secret is always a
pointer** (`env://`, or `inline:` under protest) and a **public key is always material**
(`base64:` + 44 chars). Each set must refuse the other's form, and the length cases pin 32 bytes,
which is what both Ed25519 and X25519 use — so a truncated key is rejected at write time rather
than at the first signature it fails to verify.

`links.py` exists because renaming an entity broke four anchors here and nothing failed. GitHub
derives an anchor from the heading text, so a renamed heading is a broken link at every site that
referenced it — and a broken anchor scrolls to the top of the page instead of erroring, which is
why it survives review. The subtlety: GitHub does not collapse consecutive hyphens, so
`## Delete — disabled` is `#delete--disabled` and a checker that collapses them reports false
breakage on every em-dash heading. Both directions mutation-tested. `archive/` is excluded — it is
another team's set, kept diffable against its source.

`packs.py` is the only checker whose contract lives in another repo. `resourceAttributes` is an
open container in Beckn, so nothing in [`schemas/`](../schemas) can police what goes inside one —
the constraint is the domain pack under `network-specs/schema/<Type>/vN.N/attributes.yaml`, and
its requirements are *conditional*: `WeatherObservation` with `informationMode: OnDemand` requires
the three `supported*` arrays and forbids `parameters`, while `Direct` requires `parameters`,
`source`, `location` and `generatedAt`. An advertisement that leaked a value reads perfectly fine.
Two forms must conform — what a provider **publishes** and what it **returns**. A `select` request
is neither, and `informationMode` has no legal value for a query, so it sits in `SKIP` with its
reason; a `SKIP` entry matching no payload is itself a failure, so the list cannot rot. Remote
`$ref`s (`schema.beckn.io`) are stubbed permissive rather than fetched, and a block that fails to
parse *and* contains `resourceAttributes` fails loudly instead of being skipped. The pack repo is
found at `~/Documents/Projects/OpenAgriNet/network-specs` or `$NETWORK_SPECS`; absent, the checker
prints `SKIPPED` and exits 0, so it does not block someone who only has this repo. Mutation-tested
in both directions: dropping `supportedParameters` from the advertisement and a `unit` from an
outcome parameter each produce exactly one failure.
