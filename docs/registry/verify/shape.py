"""Every record shown anywhere in this folder must be a real record, not a sketch:

  (a) it validates against its entity's schema, and
  (b) across all blocks for an entity, EVERY declared property is exercised.

A block counts as a record if its single top-level key is an entity name. Every other
```json block in these pages is a Beckn payload or an upstream response and is skipped,
so a page can show whatever it needs without tripping this.

Coverage is a union across blocks, not per block — `auth.paramName` and
`auth.paramNames` are mutually exclusive, so no single record can exercise both.

Run:  python3 verify/shape.py          (from docs/registry)
Needs: jsonschema
"""
import json, re, io, glob, sys, warnings
warnings.filterwarnings("ignore")
from jsonschema import Draft7Validator


def strip_comments(t):
    out, i, instr, esc = [], 0, False, False
    while i < len(t):
        c = t[i]
        if instr:
            out.append(c)
            if esc:      esc = False
            elif c == "\\": esc = True
            elif c == '"': instr = False
            i += 1; continue
        if c == '"':
            instr = True; out.append(c); i += 1; continue
        if c == "/" and i + 1 < len(t) and t[i + 1] == "/":
            while i < len(t) and t[i] != "\n": i += 1
            continue
        out.append(c); i += 1
    return "".join(out)


def resolve(node, sch):
    seen = set()
    while "$ref" in node:
        name = node["$ref"].split("/")[-1]
        if name in seen: return {}
        seen.add(name)
        node = sch["definitions"][name]
    return node


def declared(node, sch, where="", depth=0):
    """Every property path the schema declares, recursively."""
    if depth > 6: return set()
    node = resolve(node, sch)
    acc = set()
    for name, sub in node.get("properties", {}).items():
        path = f"{where}.{name}" if where else name
        acc.add(path)
        acc |= declared(sub, sch, path, depth + 1)
    return acc


def covered(inst, node, sch, where="", depth=0):
    """Every property path this record actually sets."""
    if depth > 6 or not isinstance(inst, dict): return set()
    node = resolve(node, sch)
    acc = set()
    for name, sub in node.get("properties", {}).items():
        if name not in inst: continue
        path = f"{where}.{name}" if where else name
        acc.add(path)
        acc |= covered(inst[name], sub, sch, path, depth + 1)
    return acc


def main():
    schemas = {f.split("/")[-1][:-5]: json.load(io.open(f, encoding="utf-8"))
               for f in glob.glob("schemas/*.json")}
    pages = sorted(f for f in glob.glob("*.md") if f != "README.md")
    blocks = []
    for page in pages:
        md = io.open(page, encoding="utf-8").read()
        for b in re.findall(r"```jsonc?\n(.*?)```", md, re.S):
            blocks.append((page, b))
    print(f"pages scanned: {', '.join(pages)}")

    fail, seen, checked = 0, {}, 0
    for page, b in blocks:
        try:
            rec = json.loads(strip_comments(b))
        except Exception:
            continue                      # not JSON, or a fragment — not a record claim
        if not isinstance(rec, dict) or len(rec) != 1:
            continue
        entity = list(rec)[0]
        if entity not in schemas:
            continue                      # a Beckn payload or an upstream response
        checked += 1
        sch = schemas[entity]
        errs = sorted(Draft7Validator(sch).iter_errors(rec), key=lambda x: list(x.path))
        if errs:
            fail += 1
            print(f"  INVALID  {page}  {entity}: {len(errs)} error(s)")
            for e in errs[:5]:
                print(f"      {'.'.join(map(str, e.path)) or '(root)'}: {e.message[:100]}")
        else:
            print(f"  valid    {page}  {entity}")
        s = seen.setdefault(entity, set())
        s |= covered(rec[entity], sch["definitions"][entity], sch)

    print(f"\nrecord blocks checked: {checked}\n")
    for entity, sch in sorted(schemas.items()):
        if entity not in seen:
            print(f"  NO RECORD BLOCK for {entity}"); fail += 1; continue
        want = declared(sch["definitions"][entity], sch)
        gap = sorted(want - seen[entity])
        if gap:
            fail += 1
            print(f"  {entity}: {len(gap)} of {len(want)} properties never exercised: {gap}")
        else:
            print(f"  OK  {entity}: all {len(want)} declared properties exercised across its blocks")

    print(f"\nfailures: {fail}")
    return 1 if fail else 0


if __name__ == "__main__":
    sys.exit(main())
