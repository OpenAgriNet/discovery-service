# Doc checkers

Run from `docs/registry`. Four need `jsonschema` (`packs.py` also needs `pyyaml`);
`links.py` needs nothing:

```
python3 verify/shape.py         # every record shown in any page here is a real record
python3 verify/records.py       # the records in examples.md, plus the five rules
python3 verify/cases.py         # what `type` decides, and the key-material grammar
python3 verify/links.py         # every `](file.md#anchor)` in this folder resolves
python3 verify/packs.py         # every resource shown here satisfies its network-specs domain pack
```

All five exit non-zero on failure, so they drop into CI unchanged.

`shape.py` scans every `.md` here except `README.md`, and treats a `json` block as a record claim
when its single top-level key is an entity name — so a page can show Beckn payloads and upstream
responses freely without tripping it. Each record must validate, and — **unioned across an
entity's blocks, and across an array's entries** — every declared property must be exercised. The
walk follows `items` as well as `properties`, so a field that exists only inside `actions[]` still
has to appear somewhere on these pages; without that it stopped at the array and everything inside
counted as covered by having shown the array at all. Mutation-tested — deleting `retryMax` from
every `actions[]` entry reports `actions[].retryMax` and nothing else. The union matters: `role` and
`keys` exist only on a node and are refused on an upstream, so no single record can cover the whole
schema, and a per-block rule would force the docs to document one half and stop documenting the
other.

`shape.py` also checks `_osConfig`, which no record can exercise because it is RC configuration
rather than schema — and which therefore rots in silence. A `privateFields` path naming a field
that no longer exists matches nothing, and nothing errors; the flatten left
`$.upstream.auth.secrets` behind exactly that way. Dropping `auth` removed the last thing a path
could name, so `privateFields` is now absent from all three schemas rather than stale in one — and
the check's job here is to make the *next* one loud, on the day a field worth redacting returns.
`indexFields` is the other half: it is what RC will actually let an operator filter on, api.md states it in a table, and
nothing compared the two. Both directions mutation-tested — reverting the path, dropping a field
from the schema, and adding one to the table each produce exactly one failure.

`records.py` catches what a schema cannot. JSON Schema cannot compare two fields and RC enforces
no reference between entities, so five rules live here instead. Each is a record that passes every
pattern in its schema and still produces a failed call weeks later.

1. **`bindingKey` equals `participantId` + `"|"` + `capabilityCode`.**
2. **Both halves resolve to `active` records, and the Participant is an `upstream`.** A binding
   says how to call an API. A node is not one — its `baseUrl` takes Beckn actions, not a
   binding's `path` — so the binding resolves to a call that cannot be made.
3. **`version` equals the `vN.N` segment of `schemaUrl`.** Otherwise a record advertises `v0.1`
   and resolves `v0.2`.
4. **One `actions[]` entry per action.** `uniqueItems` compares whole objects, so two `select`
   entries with different paths both validate and the adapter calls whichever it indexed first.
5. **A mapping filename's action segment equals the `action` it sits under.** Both are valid in
   isolation; disagreeing applies a correct mapping to the wrong call, which returns a
   well-shaped answer to a question nobody asked.

Rule 4 is not violated by any seeded record, so it was verified by mutation: duplicate the `select`
entry in a copy of `examples.md` and confirm the checker reports rule 4 and not another.

`records.py` also carries one invariant that is not one of the five: **no record holds a
credential.** There is no field for one — the credential for calling an upstream belongs to the
binding plugin's environment — and `additionalProperties: false` refuses a field named `auth`. What
nothing refuses is a secret smuggled into a field that *is* declared: a token in a `baseUrl` query,
a password pasted into a `name`. So the check walks **every string in every record** and fails on
`env://` or `inline:` anywhere. It is deliberately stricter than the old rule, which looked only at
`auth.secrets` and would have passed all of those. Mutation-tested — a pointer dropped into a
`name` produces exactly one failure.

`cases.py` (formerly `auth_cases.py`) carries negative cases as well as positive ones. A case that
passes for the wrong reason is worse than no case: a negative that `additionalProperties: false`
refuses outright pins nothing about the rule it claims to test, so each is checked against a schema
mutated to remove the rule and confirmed to fail for the intended reason.

Its `SHAPE` block pins what `type` decides. None of it can be shown by a record in `examples.md`,
because a valid record cannot demonstrate an illegal combination — an upstream carrying `keys`, a
node id that is not a hostname, a node over plaintext http. Five of the twenty guard a revert rather
than a bug: `subscriberId` reappearing, either wrapper object — `node` or `upstream` — coming back,
and `auth` coming back on either half. A sixth guards `keys` going back to an array. Mutation-tested
against the schema itself: deleting the two `if/then` branches fails 6 of the 20, relaxing
`additionalProperties` fails the 5 revert guards and nothing else, and restoring `keys` as an array
of `PublicKey` fails 3 — the array guard plus the two node records that are no longer valid, which
is the shape of that revert stated twice.

One case is a **positive with nothing behind it**: `upstream over plaintext http` passes because,
with no `auth` field, the schema cannot tell whether a credential rides on the call and so cannot
condition the transport rule on one. The guard that used to sit here — https unless
`auth.scheme` was `none` — is gone, and it now lives in the binding's plugin, where nothing in this
folder can assert it.

Its `NARROWING` block pins the key-material grammar: a **public key is always material**
(`base64:` + 44 chars), and both credential forms — `env://` and `inline:` — are refused, so the
field cannot quietly become a place to put one. The length cases pin 32 bytes, which is what both
Ed25519 and X25519 use, so a truncated key is rejected at write time rather than at the first
signature it fails to verify. It has one half now; a secret is not expressible in these schemas at
all, and the `records.py` invariant above is what asserts that.

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
