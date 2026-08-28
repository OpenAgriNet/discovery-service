# Doc checkers

Run from `docs/registry`. They need `jsonschema` and nothing else:

```
python3 verify/shape.py         # the ```jsonc shape blocks in registry.md are real records
python3 verify/auth_cases.py    # the per-scheme auth matrix in schemas/Provider.json
```

Both exit non-zero on failure, so they drop into CI unchanged.

`shape.py` asserts two things about every shape block: it strips to valid JSON and
validates against its entity's schema, and — unioned across an entity's blocks —
every declared property is exercised. The union matters: `auth.paramName` and
`auth.paramNames` are mutually exclusive, so no single record can cover both, and a
per-block rule would force the doc to choose one and stop documenting the other.

`auth_cases.py` carries the negative cases as well as the positive ones. A case that
passes for the wrong reason is worse than no case — before `paramNames` existed, every
`paramNames` negative passed because `additionalProperties: false` refused the field
outright. They are kept so the same cases now fail for the intended reason instead.
