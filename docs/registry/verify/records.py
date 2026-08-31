"""The records in examples.md are write bodies, so they must be valid write bodies:

  (a) each ```json block that names an entity validates against schemas/<Entity>.json, and
  (b) the eight rules of verify/README.md hold across the whole set — the ones draft-07
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
            continue                      # fragments (a bare auth object) are illustrative
        if isinstance(rec, dict) and len(rec) == 1 and list(rec)[0] in schemas:
            yield list(rec)[0], rec


def rules(by_entity, fail):
    """The eight rules of verify/README.md — what JSON Schema and RC cannot express."""
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
                # takes Beckn actions, not a binding's path, and it carries no
                # credential of ours, so this resolves to a call that cannot be made.
                fail(f'rule 2: {key} names Participant {ref!r}, which is a '
                     f'{row.get("type")}, not an upstream')

        seen_actions = []
        for i, a in enumerate(b.get("actions", [])):
            act = a.get("action")
            where = f'{key} actions[{i}]'

            # rule 6 — an enricher's config holds no address. config is the one free-form
            # object in all three schemas, so it is the one place a literal DSN can be pasted
            # where an env:// pointer belongs. Anything address-shaped goes in secrets.
            for name, val in a.get("enricher", {}).get("config", {}).items():
                if isinstance(val, str) and "://" in val:
                    fail(f'rule 6: {where} enricher.config.{name} looks like an address '
                         f'({val.split("://")[0]}://…) — that belongs in enricher.secrets')

            # rule 7 — one entry per action. uniqueItems compares whole objects, so two
            # entries for the same action with different paths both validate and the
            # adapter takes whichever it indexed first. Same failure as rule 5's keyId.
            seen_actions.append(act)

            # rule 8 — the mapping filename's action segment equals the action it sits
            # under. Both are correct in isolation; disagreeing applies a valid mapping
            # to the wrong call, which returns a shaped answer to the wrong question.
            seg = re.search(r"\.([a-z_]+)\.ya?ml$", a.get("mappings", ""))
            if seg is None:
                fail(f'rule 8: {where} mappings has no action segment')
            elif seg.group(1) != act:
                fail(f'rule 8: {where} is action {act!r} but its mapping is '
                     f'{seg.group(1)!r} — {a["mappings"]}')

        dupes = sorted({a for a in seen_actions if seen_actions.count(a) > 1})
        if dupes:
            fail(f'rule 7: {key} repeats action(s) {dupes} in actions[]')

    for p in by_entity.get("Participant", []):
        auth = p.get("auth", {})
        # rule 3 — paramNames keys are exactly the secrets keys
        if "paramNames" in auth:
            if set(auth["paramNames"]) != set(auth.get("secrets", {})):
                fail(f'rule 3: {p["participantId"]} paramNames keys '
                     f'{sorted(auth["paramNames"])} != secrets keys '
                     f'{sorted(auth.get("secrets", {}))}')
        # rule 5 — keyId unique within keys
        ids = [k["keyId"] for k in p.get("keys", [])]
        if len(ids) != len(set(ids)):
            fail(f'rule 5: {p["participantId"]} repeats a keyId in keys')

    # not a §3.4 rule — an invariant of this file being COMMITTED. The schema
    # permits inline:, because an operator who cannot set an environment needs
    # it; a reviewed document in git is never that operator.
    for p in by_entity.get("Participant", []):
        for name, ref in p.get("auth", {}).get("secrets", {}).items():
            if not ref.startswith("env://"):
                fail(f'committed docs: {p["participantId"]} secrets.{name} is '
                     f'{ref.split(":")[0]}:… — only env:// belongs in a tracked file')
    # enricher.secrets is env://-only in the schema itself, so there is nothing to add
    # here — the pattern refuses inline: at write time, not merely in a tracked file.

    for c in by_entity.get("SchemaRegistry", []):
        # rule 4 — version equals the vN.N segment of schemaUrl
        seg = re.search(r"/(v[0-9]+\.[0-9]+)/", c["schemaUrl"])
        if seg is None:
            fail(f'rule 4: {c["capabilityCode"]} schemaUrl has no version segment')
        elif seg.group(1) != c["version"]:
            fail(f'rule 4: {c["capabilityCode"]} says {c["version"]!r} '
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
