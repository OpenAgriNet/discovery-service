# Doc checkers

Run from `docs/registry`. Four need `jsonschema` (`packs.py` also needs `pyyaml`);
`links.py` needs nothing:

```
python3 verify/shape.py         # every record shown in any page here is a real record
python3 verify/records.py       # the records in examples.md, plus the eight rules
python3 verify/auth_cases.py    # what `type` decides, the auth matrix, the material grammar
python3 verify/links.py         # every `](file.md#anchor)` in this folder resolves
python3 verify/packs.py         # every resource shown here satisfies its network-specs domain pack
```

All five exit non-zero on failure, so they drop into CI unchanged.

`shape.py` scans every `.md` here except `README.md`, and treats a `json` block as a record claim
when its single top-level key is an entity name — so a page can show Beckn payloads and upstream
responses freely without tripping it. Each record must validate, and — **unioned across an
entity's blocks, and across an array's entries** — every declared property must be exercised. The
walk follows `items` as well as `properties`, so a field that exists only inside `actions[]` or
`keys[]` still has to appear somewhere on these pages; without that it stopped at the array and
everything inside counted as covered by having shown the array at all. Mutation-tested — deleting
`retryMax` from every `actions[]` entry reports `actions[].retryMax` and nothing else. The union
matters:
`auth.paramName` and `auth.paramNames` are mutually exclusive, so no single record can cover
both, and a per-block rule would force the docs to document one and stop documenting the other.
`examples.md` carries two probe records under *Forms no seeded record uses* to keep `paramNames`
and `valuePrefix` exercised, since no v1 record uses either.

`shape.py` also checks `_osConfig`, which no record can exercise because it is RC configuration
rather than schema — and which therefore rots in silence. A `privateFields` path naming a field
that no longer exists matches nothing, so the secret is returned in the clear with no error
anywhere; the flatten left `$.upstream.auth.secrets` behind exactly that way. `indexFields` is the
other half: it is what RC will actually let an operator filter on, api.md states it in a table, and
nothing compared the two. Both directions mutation-tested — reverting the path, dropping a field
from the schema, and adding one to the table each produce exactly one failure.

`records.py` catches what a schema cannot. JSON Schema cannot compare two fields and RC enforces
no reference between entities, so eight rules live here instead. Each is a record that passes every
pattern in its schema and still produces a failed call, or a silently unverifiable signature,
weeks later.

1. **`bindingKey` equals `participantId` + `"|"` + `capabilityCode`.**
2. **Both halves resolve to `active` records, and the Participant is an `upstream`.** A binding
   says how to call an API. A node is not one — its `baseUrl` takes Beckn actions, not a
   binding's `path` — so the binding resolves to a call that cannot be made.
3. **Where `auth.paramNames` is used, its keys are exactly the keys of `auth.secrets`.** A name
   with no secret sends an empty header; a secret with no name is never sent.
4. **`version` equals the `vN.N` segment of `schemaUrl`.** Otherwise a record advertises `v0.1`
   and resolves `v0.2`.
5. **`keys[].keyId` is unique within the array.** `uniqueItems` compares whole objects, so two
   entries with the same `keyId` and different material both pass, and a verifier gets whichever
   it found first.
6. **No `enricher.config` value is an address.** `config` is the only free-form object in the
   three schemas, so it is the only place a literal DSN can be pasted where an `env://` pointer
   belongs. Anything containing `://` goes in `enricher.secrets`.
7. **One `actions[]` entry per action.** `uniqueItems` compares whole objects — the same problem
   as rule 5 — so two `select` entries with different paths both validate and the adapter calls
   whichever it indexed first.
8. **A mapping filename's action segment equals the `action` it sits under.** Both are valid in
   isolation; disagreeing applies a correct mapping to the wrong call, which returns a
   well-shaped answer to a question nobody asked.

`paramNames` and `keyId` uniqueness are not violated by any seeded record, so they were verified
by mutation: inject the violation into a copy of `examples.md` and confirm the checker reports
that rule and not another.

`records.py` also carries one invariant that is not one of the eight: **no secret in `examples.md`
may be anything but `env://`.** The schema permits `inline:` on purpose — an operator who cannot
set an environment variable needs it — but a reviewed file in git is never that operator, and the
day someone pastes a working key into an example is the day it is in every clone.
Mutation-tested.

`auth_cases.py` carries negative cases as well as positive ones. A case that passes for the wrong
reason is worse than no case — before `paramNames` existed, every `paramNames` negative passed
because `additionalProperties: false` refused the field outright. They are kept so the same cases
now fail for the intended reason.

Its `SHAPE` block pins what `type` decides. None of it can be shown by a record in `examples.md`,
because a valid record cannot demonstrate an illegal combination — a node carrying `auth`, an
upstream carrying `keys`, a node id that is not a hostname, a credential over plaintext http.
Three of them guard a revert rather than a bug: `subscriberId` reappearing, and either
wrapper object — `node` or `upstream` — coming back. Mutation-tested against the schema
itself — deleting the two `if/then` branches fails 8 of the 18, and relaxing
`additionalProperties` fails the 3 revert guards.

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
