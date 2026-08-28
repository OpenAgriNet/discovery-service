# Doc checkers

Run from `docs/registry`. Three need `jsonschema`; `links.py` needs nothing:

```
python3 verify/shape.py         # the ```jsonc shape blocks in registry.md are real records
python3 verify/records.py       # the 13 records in examples.md, plus the §3.4 rules
python3 verify/auth_cases.py    # the per-scheme auth matrix in schemas/Participant.json
python3 verify/links.py         # every `](file.md#anchor)` in this folder resolves
```

All four exit non-zero on failure, so they drop into CI unchanged.

`shape.py` asserts two things about every shape block: it strips to valid JSON and
validates against its entity's schema, and — unioned across an entity's blocks —
every declared property is exercised. The union matters: `auth.paramName` and
`auth.paramNames` are mutually exclusive, so no single record can cover both, and a
per-block rule would force the doc to choose one and stop documenting the other.

`records.py` is the one that catches what a schema cannot. Validating the thirteen records
is the easy half; the half that matters is the five rules of
[registry.md §3.4](../registry.md#34-five-rules-the-schema-cannot-express) — `bindingKey`
against its own two fields, references that resolve to live records, `paramNames` against
`secrets`, `version` against `schemaUrl`, `keyId` uniqueness. Each of those is a record that
passes every pattern in its schema and still produces a failed call, or a silently
unverifiable signature, some weeks later.

Two of the five — `paramNames` and `keyId` — are not exercised by any seeded record, because
no v1 participant uses either field. They were verified by mutation instead: injecting the
violation into a copy of `examples.md` and confirming the checker reports that rule and not
another.

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
