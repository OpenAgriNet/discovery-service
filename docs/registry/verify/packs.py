"""Every resource this folder shows must satisfy the network's own domain pack.

`resourceAttributes` is an open container in Beckn, so nothing in `schemas/` can
police what goes inside it — the constraint lives in a different repo. The packs
carry conditional requirements that eyeballing a payload will not catch:
`WeatherObservation` with `informationMode: OnDemand` REQUIRES the three
`supported*` arrays and FORBIDS `parameters`, while `Direct` requires
`parameters`, `source`, `location` and `generatedAt`. An advertisement that
leaked a value, or an outcome that forgot its units, reads fine and is wrong.

Two forms must conform: what a provider PUBLISHES (the `on_discover`
advertisement) and what it RETURNS (the `on_select` outcome). A `select` request
is neither — the pack has no query form, and `informationMode` is required, so no
legal value exists for one. Those are listed in SKIP, by id and reason.

The packs live in a sibling repo, so this checker is conditional on finding it:
  NETWORK_SPECS=/path/to/network-specs python3 verify/packs.py
Default: ~/Documents/Projects/OpenAgriNet/network-specs

Run:  python3 verify/packs.py     (from docs/registry)
Needs: jsonschema, pyyaml
"""
import json, re, io, os, glob, sys, warnings
warnings.filterwarnings("ignore")
import yaml
from jsonschema import Draft202012Validator

NS = os.environ.get("NETWORK_SPECS",
                    os.path.expanduser("~/Documents/Projects/OpenAgriNet/network-specs"))

# A payload that deliberately does not conform, and why. Anything not listed here
# is expected to pass, so a new non-conforming payload fails rather than passing quietly.
SKIP = {
    "res:mausamgram:point-forecast|None":
        "the select REQUEST form — a query is neither the pack's advertisement "
        "nor its outcome, and informationMode has no legal value for one",
}


def load(path):
    return yaml.safe_load(io.open(path, encoding="utf-8"))


def deref(node, base, depth=0):
    """Inline every $ref. Local file refs are resolved; remote ones are stubbed
    permissive, so a missing network fetch cannot turn into a false failure."""
    if depth > 12:
        return {}
    if isinstance(node, list):
        return [deref(x, base, depth + 1) for x in node]
    if not isinstance(node, dict):
        return node
    if "$ref" in node:
        ref = node["$ref"]
        if ref.startswith("http"):
            return {}                        # e.g. schema.beckn.io GeoJSONGeometry
        path, _, frag = ref.partition("#")
        tgt = os.path.normpath(os.path.join(os.path.dirname(base), path)) if path else base
        doc = load(tgt)
        for seg in [s for s in frag.split("/") if s]:
            doc = doc[seg]
        return deref(doc, tgt, depth + 1)
    return {k: deref(v, base, depth + 1) for k, v in node.items()}


def validator_for(typename):
    """openagrinet:WeatherObservation -> schema/WeatherObservation/vN.N/attributes.yaml"""
    short = typename.split(":")[-1]
    hits = sorted(glob.glob(f"{NS}/schema/{short}/v*/attributes.yaml"))
    if not hits:
        return None, None
    root = hits[-1]                          # highest version present
    doc = load(root)
    node = doc.get("components", {}).get("schemas", {}).get(short)
    if node is None:
        return None, root
    return Draft202012Validator(deref(node, root)), root


def resources(doc):
    """Every (id, resourceAttributes) pair anywhere in a payload."""
    out = []
    def walk(n):
        if isinstance(n, list):
            for x in n:
                walk(x)
        elif isinstance(n, dict):
            ra = n.get("resourceAttributes")
            if isinstance(ra, dict) and isinstance(ra.get("@type"), str):
                out.append((n.get("id", "?"), ra))
            for v in n.values():
                walk(v)
    walk(doc)
    return out


def main():
    if not os.path.isdir(f"{NS}/schema"):
        print(f"SKIPPED: no pack repo at {NS}/schema")
        print("         set NETWORK_SPECS to run this checker")
        return 0

    print(f"packs from: {NS}")
    cache, checked, fail, skipped = {}, 0, 0, 0
    seen_skips = set()

    for page in sorted(glob.glob("*.md")):
        md = io.open(page, encoding="utf-8").read()
        for block in re.findall(r"```jsonc?\n(.*?)```", md, re.S):
            body = block[block.index("{"):] if "{" in block else block
            try:
                doc = json.loads(body)
            except Exception:
                # A record, a jsonc block with comments, or a JSONata expression.
                # None of those carry a resource — but if one does, it was meant to
                # be checked and silently skipping it is the failure mode to avoid.
                if '"resourceAttributes"' in body:
                    fail += 1
                    head = body.strip().splitlines()[0][:60]
                    print(f"  UNPARSED  {page}  block holds resourceAttributes: {head}")
                continue
            for rid, ra in resources(doc):
                tname = ra["@type"]
                tag = f'{rid} mode={ra.get("informationMode")}'
                key = f'{rid}|{ra.get("informationMode")}'
                if key in SKIP:
                    skipped += 1
                    seen_skips.add(key)
                    print(f"  skip  {page}  {tag}\n        {SKIP[key]}")
                    continue
                if tname not in cache:
                    cache[tname] = validator_for(tname)
                val, root = cache[tname]
                if val is None:
                    fail += 1
                    print(f"  NO PACK  {page}  {tag}  ({tname})")
                    continue
                checked += 1
                errs = sorted(val.iter_errors(ra), key=lambda e: list(e.path))
                if errs:
                    fail += 1
                    print(f"  FAIL  {page}  {tag}")
                    for e in errs[:5]:
                        where = "/".join(map(str, e.path)) or "(root)"
                        print(f"        {where}: {e.message[:130]}")
                else:
                    print(f"  ok    {page}  {tag}")

    for key in sorted(set(SKIP) - seen_skips):
        fail += 1
        print(f"  STALE SKIP  {key} matches no payload — delete it from SKIP")

    print(f"\nresources checked: {checked}   skipped: {skipped}   failing: {fail}")
    return 1 if fail else 0


if __name__ == "__main__":
    sys.exit(main())
