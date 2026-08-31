"""The records in examples.md are write bodies, so they must be valid write bodies:

  (a) each ```json block that names an entity validates against schemas/<Entity>.json, and
  (b) the five rules of verify/README.md hold across the whole set — the ones draft-07
      cannot express, which are therefore the ones nothing else checks.

(b) is the point. A record can satisfy every pattern in its schema and still be wrong in a
way that only shows up as a failed upstream call weeks later: a bindingKey that disagrees
with its own two fields, a version that disagrees with its own URL, a dangling reference.

Run:  python3 verify/records.py          (from docs/registry)
Needs: jsonschema
"""
import json, re, io, glob, sys, warnings
warnings.filterwarnings("ignore")
from jsonschema import Draft7Validator

MD = "examples.md"


def load_schemas():
    return {f.split("/")[-1][:-5]: json.load(io.open(f, encoding="utf-8"))
            for f in glob.glob("schemas/*.json")}


def records(schemas):
    md = io.open(MD, encoding="utf-8").read()
    for body in re.findall(r"```json\n(.*?)```", md, re.S):
        try:
            rec = json.loads(body)
        except Exception:
            continue                      # fragments are illustrative, not records
        if isinstance(rec, dict) and len(rec) == 1 and list(rec)[0] in schemas:
            yield list(rec)[0], rec


def rules(by_entity, fail):
    """The five rules of verify/README.md — what JSON Schema and RC cannot express."""
    participants = {r["participantId"]: r for r in by_entity.get("Participant", [])}
    caps = {r["capabilityCode"]: r for r in by_entity.get("SchemaRegistry", [])}

    for b in by_entity.get("ProviderSchema", []):
        key = b["bindingKey"]
        # rule 1 — bindingKey is participantId + "|" + capabilityCode
        want = f'{b["participantId"]}|{b["capabilityCode"]}'
        if key != want:
            fail(f'rule 1: bindingKey {key!r} should be {want!r}')
        # rule 2 — both references resolve, and to live records
        for label, table, ref in (("Participant", participants, b["participantId"]),
                                  ("SchemaRegistry", caps, b["capabilityCode"])):
            row = table.get(ref)
            if row is None:
                fail(f'rule 2: {key} names {label} {ref!r}, which is not in {MD}')
            elif row["status"] != "active":
                fail(f'rule 2: {key} names {label} {ref!r}, which is {row["status"]}')
            elif label == "Participant" and row.get("type") != "upstream":
                # A binding says how to call an API. A node is not one — its baseUrl
                # takes Beckn actions, not a binding's path — so this resolves to a
                # call that cannot be made.
                fail(f'rule 2: {key} names Participant {ref!r}, which is a '
                     f'{row.get("type")}, not an upstream')

        seen_actions = []
        for i, a in enumerate(b.get("actions", [])):
            act = a.get("action")
            where = f'{key} actions[{i}]'

            # rule 4 — one entry per action. uniqueItems compares whole objects, so two
            # entries for the same action with different paths both validate and the
            # adapter takes whichever it indexed first.
            seen_actions.append(act)

            # rule 5 — the mapping filename's action segment equals the action it sits
            # under. Both are correct in isolation; disagreeing applies a valid mapping
            # to the wrong call, which returns a shaped answer to the wrong question.
            seg = re.search(r"\.([a-z_]+)\.ya?ml$", a.get("mappings", ""))
            if seg is None:
                fail(f'rule 5: {where} mappings has no action segment')
            elif seg.group(1) != act:
                fail(f'rule 5: {where} is action {act!r} but its mapping is '
                     f'{seg.group(1)!r} — {a["mappings"]}')

        dupes = sorted({a for a in seen_actions if seen_actions.count(a) > 1})
        if dupes:
            fail(f'rule 4: {key} repeats action(s) {dupes} in actions[]')

    # not one of the five — the invariant that the credential really did leave the
    # registry. additionalProperties:false already refuses a field named `auth`, but
    # nothing refuses a secret smuggled into a field that IS declared: a name, a
    # baseUrl with a token in its query, a path. The property is "no record holds a
    # credential", so check every string in every record rather than one field.
    def strings(node, where=""):
        if isinstance(node, dict):
            for k, v in node.items():
                yield from strings(v, f"{where}.{k}" if where else k)
        elif isinstance(node, list):
            for i, v in enumerate(node):
                yield from strings(v, f"{where}[{i}]")
        elif isinstance(node, str):
            yield where, node

    for entity, rows in sorted(by_entity.items()):
        for row in rows:
            ident = (row.get("participantId") or row.get("capabilityCode")
                     or row.get("bindingKey"))
            for where, val in strings(row):
                for form in ("env://", "inline:"):
                    if form in val:
                        fail(f'no credential in the registry: {entity} {ident} {where} '
                             f'contains {form!r} — a credential belongs to the binding\'s '
                             f'plugin environment, not to a record')

    for c in by_entity.get("SchemaRegistry", []):
        # rule 3 — version equals the vN.N segment of schemaUrl
        seg = re.search(r"/(v[0-9]+\.[0-9]+)/", c["schemaUrl"])
        if seg is None:
            fail(f'rule 3: {c["capabilityCode"]} schemaUrl has no version segment')
        elif seg.group(1) != c["version"]:
            fail(f'rule 3: {c["capabilityCode"]} says {c["version"]!r} '
                 f'but resolves {seg.group(1)!r}')


def main():
    schemas = load_schemas()
    fails = []
    fail = fails.append

    by_entity = {}
    for entity, rec in records(schemas):
        errs = sorted(Draft7Validator(schemas[entity]).iter_errors(rec),
                      key=lambda x: list(x.path))
        ident = (rec[entity].get("participantId") or rec[entity].get("capabilityCode")
                 or rec[entity].get("bindingKey"))
        if errs:
            for e in errs[:3]:
                fail(f'{entity} {ident}: {".".join(map(str, e.path)) or "(root)"}: '
                     f'{e.message[:120]}')
        else:
            print(f"  valid    {entity:15} {ident}")
        by_entity.setdefault(entity, []).append(rec[entity])

    counts = {e: len(v) for e, v in sorted(by_entity.items())}
    print(f"\nrecords: {counts}  total {sum(counts.values())}")

    rules(by_entity, fail)
    for f in fails:
        print(f"  FAIL  {f}")
    print(f"\n{MD}: {len(fails)} failing")
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(main())
