# Doc checkers

Run from `docs/registry`. Three need `jsonschema`; `links.py` needs nothing:

```
python3 verify/shape.py         # every record shown in any page here is a real record
python3 verify/records.py       # the 13 records in examples.md, plus the §3.4 rules
python3 verify/auth_cases.py    # the per-scheme auth matrix in schemas/Participant.json
python3 verify/links.py         # every `](file.md#anchor)` in this folder resolves
```

All four exit non-zero on failure, so they drop into CI unchanged.

`shape.py` scans every `.md` in this folder except `README.md`, and treats a `json` block as a
record claim when its single top-level key is an entity name — so a page can show Beckn payloads
and upstream responses freely without tripping it. Each record must validate against its entity's
schema, and — unioned across an entity's blocks — every declared property must be exercised. The
union matters: `auth.paramName` and `auth.paramNames` are mutually exclusive, so no single record
can cover both, and a per-block rule would force the docs to choose one and stop documenting the
other. `schemas.md` carries two examples that exist only to keep `paramNames`, `valuePrefix` and
`publicKeys` exercised, since no v1 record uses them.

It used to read one file, `registry.md`, and only its annotated `jsonc` blocks. Widening it to
the whole folder is why a record pasted into a use case is now checked too — three of the ones
in `usecases.md` were invented before this ran over them.

`records.py` is the one that catches what a schema cannot. Validating the thirteen records
is the easy half; the half that matters is the [five rules
schemas.md](../schemas.md#five-rules-the-schema-cannot-express) names — `bindingKey`
against its own two fields, references that resolve to live records, `paramNames` against
`secrets`, `version` against `schemaUrl`, `keyId` uniqueness. Each of those is a record that
passes every pattern in its schema and still produces a failed call, or a silently
unverifiable signature, some weeks later.

Two of the five — `paramNames` and `keyId` — are not exercised by any seeded record, because
no v1 participant uses either field. They were verified by mutation instead: injecting the
violation into a copy of `examples.md` and confirming the checker reports that rule and not
another.

`records.py` also carries one invariant that is not a §3.4 rule: **no secret in
`examples.md` may be anything but `env://`.** The schema permits `inline:` on purpose — an
operator who cannot set an environment variable needs it — but a reviewed file in git is never
that operator, and the day someone pastes a working key into an example is the day it is in
every clone. Mutation-tested.

`auth_cases.py` gained the `NARROWING` block when `MaterialRef` was introduced. `Secret` and
`KeyHash` are that one grammar intersected with the schemes allowed at each site, and the
cases assert the intersection refuses the *other* site's scheme. Without them, widening
`MaterialRef` to add `vault://` would quietly make it legal as a public-key hash — the
generalisation buying reach it was never meant to buy.

`links.py` exists because renaming an entity broke four anchors in this folder and
nothing failed. GitHub derives an anchor from the heading text, so a renamed heading is a
broken link at every site that referenced it — and a broken anchor scrolls to the top of the
page instead of erroring, which is why it survives review. The subtlety is that GitHub does
not collapse consecutive hyphens: `### Delete — disabled` is `#delete--disabled`, so a
checker that collapses them reports false breakage on every em-dash heading in the folder.
Both directions are mutation-tested — renaming that heading reports exactly the two links to
it, and restoring it reports nothing. `archive/` is excluded: it is another team's set, kept
diffable against its source.

`auth_cases.py` carries the negative cases as well as the positive ones. A case that
passes for the wrong reason is worse than no case — before `paramNames` existed, every
`paramNames` negative passed because `additionalProperties: false` refused the field
outright. They are kept so the same cases now fail for the intended reason instead.
