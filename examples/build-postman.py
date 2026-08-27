#!/usr/bin/env python3
"""Generate the Postman collection from the example request files.

The collection is GENERATED rather than hand-maintained because the same
requests are already asserted by verify.sh. Two hand-written copies of the same
eight bodies drift, and the copy that drifts is always the one nobody runs in
CI — so the bodies here are read from the .json files verbatim and the expected
result sets are declared once, below, in EXPECT.

Regenerate after changing any example:

    python3 examples/build-postman.py
"""

import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "OpenAgriNet-discovery-service.postman_collection.json")

VILLAGE = "res-wx-village-belagavi"
POINT = "res-wx-point-dharwad"
ALERT = "res-wx-alert-statewide"

# file -> (name, expected resource ids, expected offer ids or None, note)
EXPECT = [
    (
        "02-discover-text-search.json",
        "02 Text search - irrigation spraying advisory",
        [POINT, VILLAGE],
        None,
        "The statewide alert says 'Severe weather alerting' and must NOT match. "
        "It is the control: if the lexical index were being skipped, all three "
        "would come back.",
    ),
    (
        "09-discover-text-or.json",
        "09 Text search - OR over terms, not AND",
        [ALERT, POINT, VILLAGE],
        None,
        "'irrigation' appears only in the village and point resources; "
        "'cyclone' only in the statewide alert. All three coming back is what "
        "proves lexical retrieval ORs its terms - under AND none would match. "
        "Case 02 cannot tell the two apart, so without this one the semantics "
        "were only assumed.",
    ),
    (
        "03-discover-schema-context.json",
        "03 schemaContext - WeatherAdvisoryCapability",
        [ALERT, POINT, VILLAGE],
        None,
        "schemaContext is a CONTEXT field, not an intent one - Intent is "
        "additionalProperties:false, so sending it under message.intent is a "
        "body that fails its own schema. All three resources carry the same "
        "@context, so this case pins acceptance rather than discrimination.",
    ),
    (
        "04-discover-spatial-dwithin.json",
        "04 Spatial - S_DWITHIN 25km of Dharwad",
        [POINT, VILLAGE],
        None,
        "The Dharwad Point is ~2km away and the Belagavi Polygon contains the "
        "query coordinate. The statewide alert carries only an ISO-3166-2 area "
        "CODE and no coordinates, so nothing was cell-indexed for it and it is "
        "not spatially discoverable at all - by design.",
    ),
    (
        "05-discover-spatial-intersects.json",
        "05 Spatial - S_INTERSECTS inside Belagavi only",
        [VILLAGE],
        ["offer-wx-free-tier"],
        "(74.50, 16.00) is deep inside the Belagavi polygon and ~120km from the "
        "Dharwad point. This case and case 04 have to DISAGREE - that is what "
        "separates a working predicate from one matching the whole catalog.",
    ),
    (
        "06-discover-filter-granularity.json",
        "06 Filter - geographicGranularity == Village",
        [VILLAGE],
        ["offer-wx-free-tier"],
        "A jsonpath filter rooted at the resource level.",
    ),
    (
        "07-discover-filter-cross-level.json",
        "07 Filter - cross-level, offer predicate selects a resource",
        [POINT],
        ["offer-wx-subscription"],
        "Rooted at the OFFER level and yet it narrows RESOURCES. This exercises "
        "the single composite filter_doc column (A18): under the earlier "
        "three-column design an offer-rooted predicate could not select a "
        "resource at all. If this returns all three, the composite regressed.",
    ),
]

ENVELOPE_TEST = """
const res = pm.response.json();
const req = JSON.parse(pm.request.body.raw);

pm.test("HTTP 200", () => pm.response.to.have.status(200));

pm.test("callback action is on_discover", () =>
    pm.expect(res.context.action).to.eql("on_discover"));

pm.test("version is 2.0.0", () =>
    pm.expect(res.context.version).to.eql("2.0.0"));

pm.test("transactionId and messageId are echoed", () => {
    pm.expect(res.context.transactionId).to.eql(req.context.transactionId);
    pm.expect(res.context.messageId).to.eql(req.context.messageId);
});

// A NACK arrives with its own shape and would otherwise slip past every
// assertion above that only looks at `catalogs`.
pm.test("not a NACK", () =>
    pm.expect(res.message.status, JSON.stringify(res.message.error))
      .to.not.eql("NACK"));

const resourceIds = (res.message.catalogs || [])
    .flatMap(c => c.resources || []).map(r => r.id).sort();

// EXACT, not "contains": a filter that has quietly stopped filtering still
// returns rows, and a subset assertion passes for it.
pm.test("resources are exactly " + JSON.stringify(__WANT_RES__), () =>
    pm.expect(resourceIds).to.eql(__WANT_RES__));
__OFFER_TEST__
// The service declares a missing retrieval mode rather than failing: the
// semantic mode defaults to `noop`, so this header is EXPECTED locally.
const degraded = pm.response.headers.get("X-Beckn-Degraded");
if (degraded) { console.log("degraded modes: " + degraded); }
"""

OFFER_TEST = """
const offerIds = (res.message.catalogs || [])
    .flatMap(c => c.offers || []).map(o => o.id).sort();

pm.test("offers are exactly " + JSON.stringify(__WANT_OFF__), () =>
    pm.expect(offerIds).to.eql(__WANT_OFF__));
"""

PUBLISH_TEST = """
const res = pm.response.json();
const req = JSON.parse(pm.request.body.raw);

pm.test("HTTP 200", () => pm.response.to.have.status(200));

pm.test("callback action is catalog/on_publish", () =>
    pm.expect(res.context.action).to.eql("catalog/on_publish"));

pm.test("transactionId and messageId are echoed", () => {
    pm.expect(res.context.transactionId).to.eql(req.context.transactionId);
    pm.expect(res.context.messageId).to.eql(req.context.messageId);
});

pm.test("not a NACK", () =>
    pm.expect(res.message.status, JSON.stringify(res.message.error))
      .to.not.eql("NACK"));

pm.test("the catalog was ACCEPTED with no errors", () => {
    const result = res.message.results[0];
    pm.expect(result.catalogId).to.eql("cat-ksndmc-weather-advisory");
    pm.expect(result.status, JSON.stringify(result.errors)).to.eql("ACCEPTED");
});

pm.test("three resources and one provider were indexed", () => {
    pm.expect(res.message.results[0].stats.itemCount).to.eql(3);
    pm.expect(res.message.results[0].stats.providerCount).to.eql(1);
});

// Publish is idempotent under MERGE, so running this collection twice is safe
// and the second run asserts that too.
"""

REFUSAL_TEST = """
const res = pm.response.json();

// The same intent as case 06 written WITHOUT the ?(...) filter. PostgreSQL
// runs it happily: `@?` is given a comparison, a comparison always yields an
// item, and `false` is an item - so it answers true for EVERY row and the
// caller receives the whole corpus formatted as a filtered page, with no
// error anywhere. A 400 here is the feature, not a limitation.
pm.test("HTTP 400", () => pm.response.to.have.status(400));

pm.test("refused as SCH_INVALID_JSONPATH", () =>
    pm.expect(res.message.error.code).to.eql("SCH_INVALID_JSONPATH"));

pm.test("the fault names the expression", () =>
    pm.expect(res.message.error.details.path)
      .to.eql("$.message.intent.filters.expression"));
"""

HEALTH_TEST = """
pm.test("HTTP 200", () => pm.response.to.have.status(200));
"""


def body_of(filename):
    with open(os.path.join(HERE, filename)) as handle:
        return json.dumps(json.load(handle), indent=2, ensure_ascii=False)


def request(name, method, path, raw=None, script=None, note=""):
    item = {
        "name": name,
        "request": {
            "method": method,
            "header": ([{"key": "Content-Type", "value": "application/json"}]
                       if raw else []),
            "url": {
                "raw": "{{baseUrl}}" + path,
                "host": ["{{baseUrl}}"],
                "path": [segment for segment in path.strip("/").split("/") if segment],
            },
            "description": note,
        },
    }
    if raw:
        item["request"]["body"] = {"mode": "raw", "raw": raw}
    if script:
        item["event"] = [{
            "listen": "test",
            "script": {"type": "text/javascript", "exec": script.strip("\n").split("\n")},
        }]
    return item


def main():
    health = [
        request("GET /healthz", "GET", "/healthz", script=HEALTH_TEST,
                note="Answers that the process is up. There is no Compose "
                     "healthcheck on the service because the runtime stage is "
                     "distroless/static and has no shell to run one."),
        request("GET /readyz", "GET", "/readyz", script=HEALTH_TEST,
                note="Answers that PostgreSQL is reachable."),
    ]

    publish = [
        request("01 Publish - Karnataka weather advisory catalog", "POST", "/publish",
                raw=body_of("01-publish-weather-advisory.json"),
                script=PUBLISH_TEST,
                note="One catalog, one provider, three resources and two offers, "
                     "built on the OpenAgriNet WeatherAdvisoryCapability schema. "
                     "Run this FIRST - every discover request below asserts "
                     "against exactly this data.")
    ]

    discover = []
    for filename, name, want_res, want_off, note in EXPECT:
        script = ENVELOPE_TEST.replace("__WANT_RES__", json.dumps(sorted(want_res)))
        if want_off:
            script = script.replace(
                "__OFFER_TEST__",
                OFFER_TEST.replace("__WANT_OFF__", json.dumps(sorted(want_off))))
        else:
            script = script.replace("__OFFER_TEST__", "")
        discover.append(request(name, "POST", "/discover",
                                raw=body_of(filename), script=script, note=note))

    refusals = [
        request("08 Refusal - jsonpath with no ?(...) filter", "POST", "/discover",
                raw=body_of("08-discover-invalid-jsonpath.json"),
                script=REFUSAL_TEST,
                note="Expected to FAIL with 400. See the test script for why "
                     "this shape is dangerous enough to refuse.")
    ]

    collection = {
        "info": {
            "name": "OpenAgriNet discovery-service",
            "description": (
                "Beckn v2.0.0 publish and discover against a local stack.\n\n"
                "  make run                      # brings up PostgreSQL + the service\n"
                "  newman run examples/OpenAgriNet-discovery-service.postman_collection.json\n\n"
                "Run the Publish folder first: every discover request asserts the "
                "EXACT set of resource ids that the published catalog should "
                "produce. Exact rather than 'contains', because a filter that has "
                "quietly stopped filtering still returns rows and a subset "
                "assertion passes for it.\n\n"
                "GENERATED by examples/build-postman.py from the example .json "
                "files - edit those and regenerate rather than editing here.\n\n"
                "X-Beckn-Degraded: semantic on the text-search responses is "
                "expected. The semantic embedding provider defaults to `noop`, so "
                "the service declares the mode missing and answers with the modes "
                "it does have, rather than failing the request."
            ),
            "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
        },
        "variable": [
            {"key": "baseUrl", "value": "http://localhost:8080", "type": "string"},
        ],
        "item": [
            {"name": "Health", "item": health},
            {"name": "Publish", "item": publish},
            {"name": "Discover", "item": discover},
            {"name": "Refusals", "item": refusals},
        ],
    }

    with open(OUT, "w") as handle:
        json.dump(collection, handle, indent=2, ensure_ascii=False)
        handle.write("\n")
    print("wrote", OUT)


if __name__ == "__main__":
    main()
