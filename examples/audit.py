#!/usr/bin/env python3
"""Independently verify that discover's results are ACCURATE, not just present.

verify.sh asserts that each request returns a hard-coded set of ids. That pins
regressions, but the expected sets in it came from watching the service run —
so if the service had been wrong on the day they were written, verify.sh would
have faithfully frozen the wrong answer. This script does not trust the service
at all. It reads the PUBLISHED catalog, recomputes what each intent should
match using geometry and text logic written here from scratch, and only then
compares against what came back.

Three independent checks per case:

  ORACLE   — recompute the answer from examples/01 and compare id sets
  SCHEMA   — validate the response body against beckn.yaml (the /on_discover
             and /catalog/on_publish request-body schemas, which are what this
             service returns synchronously)
  FIDELITY — every returned resource must be byte-identical to what was
             published (A17: the document is stored verbatim, and everything
             else is derived from it)

Usage:  python3 examples/audit.py            (needs jsonschema + pyyaml)
"""

import copy
import json
import math
import re
import os
import sys
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
BASE = os.environ.get("BASE", "http://localhost:8080")
SPEC = os.path.join(ROOT, "tests", "testdata", "beckn-v2.0.0.yaml")

PASS, FAIL, SKIPPED = [], [], []
YELLOW, OFF = "\033[33m", "\033[0m"


def ok(msg):
    PASS.append(msg)
    print("  \033[32mPASS\033[0m %s" % msg)


def bad(msg):
    FAIL.append(msg)
    print("  \033[31mFAIL\033[0m %s" % msg)


def post(path, body):
    req = urllib.request.Request(
        BASE + path,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req) as resp:
            return resp.status, json.loads(resp.read()), dict(resp.headers)
    except urllib.error.HTTPError as err:
        return err.code, json.loads(err.read()), dict(err.headers)


def load(name):
    with open(os.path.join(HERE, name)) as handle:
        return json.load(handle)


# ---------------------------------------------------------------- geometry
#
# Written here rather than imported so that it is genuinely independent of the
# H3 covering the service uses. Plane geometry on lon/lat with a haversine
# metric: at Karnataka's scale the error against a proper geodesic is metres,
# and every distance in these cases clears its threshold by kilometres.

EARTH_M = 6371008.8


def haversine(a, b):
    lon1, lat1 = math.radians(a[0]), math.radians(a[1])
    lon2, lat2 = math.radians(b[0]), math.radians(b[1])
    h = (math.sin((lat2 - lat1) / 2) ** 2
         + math.cos(lat1) * math.cos(lat2) * math.sin((lon2 - lon1) / 2) ** 2)
    return 2 * EARTH_M * math.asin(math.sqrt(h))


def point_in_ring(point, ring):
    """Ray casting. Returns True for points strictly inside."""
    x, y = point
    inside = False
    for i in range(len(ring) - 1):
        x1, y1 = ring[i]
        x2, y2 = ring[i + 1]
        if (y1 > y) != (y2 > y):
            xin = x1 + (y - y1) / (y2 - y1) * (x2 - x1)
            if x < xin:
                inside = not inside
    return inside


def point_to_segment_m(point, a, b):
    """Distance from point to segment, scaling longitude by cos(lat)."""
    lat0 = math.radians(point[1])
    sx = math.cos(lat0)

    def flat(p):
        return (p[0] * sx, p[1])

    px, py = flat(point)
    ax, ay = flat(a)
    bx, by = flat(b)
    dx, dy = bx - ax, by - ay
    if dx == 0 and dy == 0:
        return haversine(point, a)
    t = max(0.0, min(1.0, ((px - ax) * dx + (py - ay) * dy) / (dx * dx + dy * dy)))
    nearest = (ax + t * dx, ay + t * dy)
    return haversine(point, (nearest[0] / sx, nearest[1]))


def distance_to_geometry(point, geometry):
    """Metres from `point` to the nearest part of `geometry`; 0 if inside."""
    kind = geometry["type"]
    coords = geometry["coordinates"]
    if kind == "Point":
        return haversine(point, coords)
    if kind == "Polygon":
        if point_in_ring(point, coords[0]):
            return 0.0
        return min(point_to_segment_m(point, coords[0][i], coords[0][i + 1])
                   for i in range(len(coords[0]) - 1))
    raise AssertionError("the fixture has no %s; extend this if it grows one" % kind)


def geometries_of(resource):
    """Every GeoJSON object under resourceAttributes.coverageAreas.

    Recognised STRUCTURALLY — an object with `type` naming an RFC 7946 kind and
    a `coordinates` member — which is the same test the publish-side walk uses.
    The area-code entries alongside them have neither and are skipped, and that
    is exactly why the statewide alert has no indexed geometry.
    """
    out = []
    for area in (resource.get("resourceAttributes") or {}).get("coverageAreas") or []:
        if isinstance(area, dict) and "coordinates" in area and area.get("type") in (
                "Point", "LineString", "Polygon", "MultiPoint",
                "MultiLineString", "MultiPolygon"):
            out.append(area)
    return out


# ------------------------------------------------------------------- text


def searchable_text(resource):
    """Reproduce deriveSearchText: the name, plus every STRING VALUE in
    descriptor and resourceAttributes. Keys are excluded (they are a
    vocabulary, not content) and so are @-prefixed JSON-LD keywords."""
    words = []

    def walk(node):
        if isinstance(node, str):
            words.append(node)
        elif isinstance(node, list):
            for item in node:
                walk(item)
        elif isinstance(node, dict):
            for key, value in node.items():
                if not key.startswith("@"):
                    walk(value)

    walk(resource.get("descriptor"))
    walk(resource.get("resourceAttributes"))
    return " ".join(words).lower()


def stems(word):
    """Crude suffix folding, enough to relate 'spraying' to 'spray'."""
    word = word.lower().strip(".,;:()")
    for suffix in ("ing", "ies", "es", "s"):
        if len(word) > 4 and word.endswith(suffix):
            return word[: -len(suffix)]
    return word


def lexical_matches(resource, query):
    """True when ANY query term appears in the resource's searchable text.

    ANY rather than ALL: the service's lexical mode is an OR over terms, which
    is why a three-word query still returns a resource carrying two of them.
    """
    haystack = {stems(w) for w in searchable_text(resource).split()}
    return any(stems(term) in haystack for term in query.split())


def trigrams(text):
    """pg_trgm's tokenisation, reimplemented: lowercase, split on anything that
    is not alphanumeric, pad each word with two leading and one trailing space,
    take every 3-gram, deduplicate."""
    out = set()
    for word in re.findall(r"[0-9a-z]+", text.lower()):
        padded = "  " + word + " "
        for i in range(len(padded) - 2):
            out.add(padded[i:i + 3])
    return out


def trigram_similarity(a, b):
    """pg_trgm similarity(): shared trigrams over the union of both sets.

    This is the one place the oracle reimplements a documented algorithm rather
    than deriving ground truth from first principles, so it is the one place
    that could be wrong in the same direction as the thing it checks. It was
    validated against PostgreSQL's own similarity() over the fixture's names
    crossed with every query used here — 24 pairs, exact to 1e-6.
    """
    left, right = trigrams(a), trigrams(b)
    if not left and not right:
        return 0.0
    return len(left & right) / len(left | right)


# pg_trgm.similarity_threshold, the value `%` reads. Nothing in this service
# sets it, so it is PostgreSQL's 0.3 default — a deployment that moves it moves
# what the fuzzy mode admits, and this constant with it.
TRIGRAM_THRESHOLD = 0.3


def fuzzy_matches(resource, query):
    """The trigram mode, which gates on the resource NAME and not on the
    searchable text the lexical mode uses."""
    name = (resource.get("descriptor") or {}).get("name") or ""
    return trigram_similarity(name, query) >= TRIGRAM_THRESHOLD


def text_matches(resource, query):
    """Retrieval modes UNION; constraints intersect.

    Lexical and fuzzy are separate retrievers over one shared WHERE, fused by
    RRF — so a resource either one admits is in the answer. Modelling the
    lexical mode alone made this oracle agree with the service for eight cases
    and it was still incomplete: a query whose every term is misspelled matches
    no tsvector and still comes back through the trigram index. Case 13 is that
    query, and it is why this function is not just `lexical_matches`.
    """
    return lexical_matches(resource, query) or fuzzy_matches(resource, query)


# ----------------------------------------------------------------- filters


def eval_equality(catalog, resource, expression):
    """Evaluate the two filter shapes the examples use.

    Deliberately NOT a jsonpath engine — it understands exactly
    `$.catalogs[*].<level>[*] ? (@.a.b == "literal")` for <level> in
    resources/offers, which is all the fixture needs, and anything else raises
    rather than guessing. A silent fallback here would make the oracle agree
    with the service by accident.
    """
    head, _, predicate = expression.partition("?")
    predicate = predicate.strip()
    assert predicate.startswith("(") and predicate.endswith(")"), expression
    lhs, _, rhs = predicate[1:-1].partition("==")
    lhs, rhs = lhs.strip(), rhs.strip().strip('"')
    assert lhs.startswith("@."), expression
    path = lhs[2:].split(".")

    if ".resources[" in head:
        scopes = [resource]
    elif ".offers[" in head:
        # An offer applies to a resource when it names it, or when it names
        # nothing at all (an empty resourceIds means catalog-wide).
        scopes = [o for o in catalog.get("offers", [])
                  if not o.get("resourceIds") or resource["id"] in o["resourceIds"]]
    else:
        raise AssertionError("unsupported filter root: %s" % expression)

    for scope in scopes:
        node = scope
        for part in path:
            node = node.get(part) if isinstance(node, dict) else None
            if node is None:
                break
        if node == rhs:
            return True
    return False


# ------------------------------------------------------------------ oracle


def expected_for(intent, context, catalog):
    """Recompute the resources an intent should match, from the source data."""
    matched = []
    for resource in catalog["resources"]:
        if "textSearch" in intent and not text_matches(resource, intent["textSearch"]):
            continue

        if context.get("schemaContext"):
            declared = (resource.get("resourceAttributes") or {}).get("@context")
            # An entry is `<uri>#<@type>`; the service matches on the uri part.
            wanted = {entry.split("#")[0] for entry in context["schemaContext"]}
            if declared not in wanted:
                continue

        if "filters" in intent:
            if not eval_equality(catalog, resource, intent["filters"]["expression"]):
                continue

        keep = True
        for constraint in intent.get("spatial", []):
            geometries = geometries_of(resource)
            if not geometries:
                keep = False   # nothing indexable: not spatially discoverable
                break
            query = constraint["geometry"]
            assert query["type"] == "Point", "the fixture queries with points only"
            point = query["coordinates"]
            if constraint["op"] == "S_DWITHIN":
                limit = constraint["distanceMeters"]
                if not any(distance_to_geometry(point, g) <= limit for g in geometries):
                    keep = False
            elif constraint["op"] == "S_INTERSECTS":
                if not any(distance_to_geometry(point, g) == 0.0 for g in geometries):
                    keep = False
            else:
                raise AssertionError("unsupported op %s" % constraint["op"])
            if not keep:
                break
        if not keep:
            continue

        matched.append(resource["id"])
    return sorted(matched)


# ------------------------------------------------------------------ schema


def load_validators():
    try:
        import yaml
        from jsonschema import Draft202012Validator
    except ImportError:
        return None

    with open(SPEC) as handle:
        spec = yaml.safe_load(handle)

    def validator_for(path):
        schema = dict(spec["paths"][path]["post"]["requestBody"]
                      ["content"]["application/json"]["schema"])
        # Give the subschema the components it $refs.
        schema["components"] = spec["components"]
        schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
        return Draft202012Validator(schema)

    return {
        "on_discover": validator_for("/on_discover"),
        "catalog/on_publish": validator_for("/catalog/on_publish"),
    }


def check_schema(validators, label, action, body):
    if validators is None:
        SKIPPED.append(label)
        return
    errors = sorted(validators[action].iter_errors(body), key=lambda e: list(e.path))
    if errors:
        for err in errors[:3]:
            bad("%s — schema: %s at $.%s"
                % (label, err.message, ".".join(str(p) for p in err.path)))
    else:
        ok("%s — validates against beckn.yaml (%s)" % (label, action))


# ---------------------------------------------------------------- fidelity


def check_fidelity(label, published, response):
    """Every returned resource must equal what was published, member for member.

    This is A17 stated as a test: one row holds the object verbatim and every
    other column is derived from it. A response that quietly drops or reorders
    a member of resourceAttributes would pass every id-set assertion in
    verify.sh and still be wrong.
    """
    source = {r["id"]: r for r in published["message"]["catalogs"][0]["resources"]}
    problems = []
    for catalog in response["message"].get("catalogs", []):
        for got in catalog.get("resources", []):
            want = source.get(got["id"])
            if want is None:
                problems.append("%s was never published" % got["id"])
            elif got != want:
                want_keys, got_keys = set(want), set(got)
                if want_keys != got_keys:
                    problems.append("%s: members %s vs %s"
                                    % (got["id"], sorted(want_keys), sorted(got_keys)))
                else:
                    diff = [k for k in want_keys if want[k] != got[k]]
                    problems.append("%s: %s differ" % (got["id"], diff))
    if problems:
        for problem in problems[:4]:
            bad("%s — fidelity: %s" % (label, problem))
    else:
        ok("%s — every resource is byte-identical to what was published" % label)


# -------------------------------------------------------------------- main


def main():
    validators = load_validators()
    if validators is None:
        print("%sNOTE  jsonschema/pyyaml are missing, so the schema checks below "
              "are SKIPPED.%s\n      A skipped check is not a passing one: install "
              "them to validate against beckn.yaml.\n" % (YELLOW, OFF))

    published = load("01-publish-weather-advisory.json")
    catalog = published["message"]["catalogs"][0]

    print("--- publish")
    status, body, _ = post("/publish", published)
    if status == 200 and body["message"]["results"][0]["status"] == "ACCEPTED":
        ok("catalog ACCEPTED")
    else:
        bad("publish returned %s %s" % (status, json.dumps(body)[:200]))
        return 1
    check_schema(validators, "publish", "catalog/on_publish", body)
    print()

    cases = [
        "02-discover-text-search.json",
        "09-discover-text-or.json",
        "03-discover-schema-context.json",
        "04-discover-spatial-dwithin.json",
        "05-discover-spatial-intersects.json",
        "06-discover-filter-granularity.json",
        "07-discover-filter-cross-level.json",
        # A filter with no text and no geometry: the one intent that reaches
        # the candidates path carrying a filter. 04 and 05 reach it with a
        # geometry instead, so without this the NULL-query fallthrough could
        # stop applying filter_doc and nothing here would notice.
        "16-discover-filter-only.json",
        # Combinations. Retrieval modes union, constraints intersect — these
        # are the cases where those two rules meet, and each dimension here
        # excludes something the others admit.
        "10-discover-text-and-geo.json",
        "11-discover-geo-and-filter.json",
        "14-discover-text-and-filter.json",
        "12-discover-text-geo-filter-empty.json",
        "15-discover-text-geo-filter.json",
        "13-discover-fuzzy-typos.json",
    ]

    for name in cases:
        request = load(name)
        intent = request["message"]["intent"]
        print("--- %s" % name)

        # The oracle runs BEFORE the request, so there is no chance of it being
        # influenced by the answer it is supposed to be checking.
        want = expected_for(intent, request["context"], copy.deepcopy(catalog))

        status, body, _ = post("/discover", request)
        got = sorted(r["id"]
                     for c in body["message"].get("catalogs", [])
                     for r in c.get("resources", []))

        if got == want:
            ok("oracle agrees: %s" % (got or "[]"))
        else:
            bad("oracle says %s, service returned %s" % (want, got))

        check_schema(validators, name, "on_discover", body)
        check_fidelity(name, published, body)
        print()

    print("--- refusal")
    status, body, _ = post("/discover", load("08-discover-invalid-jsonpath.json"))
    if status == 400 and body["message"]["error"]["code"] == "SCH_INVALID_JSONPATH":
        ok("unbounded jsonpath refused with 400 SCH_INVALID_JSONPATH")
    else:
        bad("expected 400 SCH_INVALID_JSONPATH, got %s %s"
            % (status, json.dumps(body)[:200]))
    print()

    print("=" * 60)
    tally = "%d passed, %d failed" % (len(PASS), len(FAIL))
    if SKIPPED:
        # Loud, and part of the tally rather than a note scrolled off the top:
        # "24 passed" and "16 passed" both read as success, and the difference
        # between them is every schema assertion in the run.
        tally += ", %s%d SKIPPED (schema — install jsonschema + pyyaml)%s" % (
            YELLOW, len(SKIPPED), OFF)
    print(tally)
    return 1 if FAIL else 0


if __name__ == "__main__":
    sys.exit(main())
