"""Shape, auth and material-reference cases for schemas/Participant.json.

Run:  python3 verify/auth_cases.py          (from docs/registry)
Needs: jsonschema

Each case is (name, auth, want_valid). A case that passes for the wrong reason is
worse than no case, so the multi-param cases assert both halves: the well-formed
record is accepted AND every malformed neighbour is refused.

NARROWING pins the second half: `Secret` is `MaterialRef` and must stay a
pointer. Without these, widening MaterialRef to add `vault://` would silently
make a bare pasted credential legal.

SHAPE pins the third: `type` is a discriminator, and every branch it decides is a
rule no record in examples.md can demonstrate, because a valid record cannot show
an illegal combination. Three of them guard against a revert — either wrapper
object coming back, and `subscriberId` reappearing.
"""
import json, io, sys

def rec(auth):
    return {"Participant": {"participantId": "probe-x", "name": "Probe", "type": "upstream",
                            "status": "active", "baseUrl": "https://a.example/v1", "auth": auth}}

CASES = [
    # --- the four schemes, minimal legal form (regression) ---
    ("none minimal",            {"scheme": "none"}, True),
    ("apiKeyQuery minimal",     {"scheme": "apiKeyQuery", "paramName": "token",
                                 "secrets": {"token": "env://T"}}, True),
    ("apiKeyHeader minimal",    {"scheme": "apiKeyHeader", "paramName": "X-Key",
                                 "secrets": {"token": "env://T"}}, True),
    ("basic minimal",           {"scheme": "basic",
                                 "secrets": {"username": "env://U", "password": "env://P"}}, True),
    ("bearer via valuePrefix",  {"scheme": "apiKeyHeader", "paramName": "Authorization",
                                 "valuePrefix": "Bearer ", "secrets": {"token": "env://T"}}, True),
    ("valuePrefix needs the trailing space",
                                {"scheme": "apiKeyHeader", "paramName": "Authorization",
                                 "valuePrefix": "Bearer", "secrets": {"token": "env://T"}}, False),

    # --- multi-param: two credentials, each with its own header ---
    ("two headers via paramNames",
     {"scheme": "apiKeyHeader",
      "secrets":    {"token": "env://T", "account": "env://A"},
      "paramNames": {"token": "X-Api-Key", "account": "X-Account-Id"}}, True),
    ("two query params via paramNames",
     {"scheme": "apiKeyQuery",
      "secrets":    {"token": "env://T", "account": "env://A"},
      "paramNames": {"token": "api_key", "account": "account_id"}}, True),

    # --- and what it must still refuse ---
    ("two secrets with a single paramName is ambiguous",
     {"scheme": "apiKeyHeader", "paramName": "X-Key",
      "secrets": {"token": "env://T", "account": "env://A"}}, False),
    ("paramName and paramNames together",
     {"scheme": "apiKeyHeader", "paramName": "X-Key",
      "secrets": {"token": "env://T"}, "paramNames": {"token": "X-Key"}}, False),
    ("paramNames without secrets",
     {"scheme": "apiKeyHeader", "paramNames": {"token": "X-Key"}}, False),
    ("paramNames with a control char in a header name",
     {"scheme": "apiKeyHeader", "secrets": {"token": "env://T", "account": "env://A"},
      "paramNames": {"token": "X-Api-Key", "account": "X-Bad\r\nInjected"}}, False),
    ("paramNames with a bare pasted secret",
     {"scheme": "apiKeyHeader", "secrets": {"token": "env://T", "account": "sk-live-abc"},
      "paramNames": {"token": "X-Api-Key", "account": "X-Account-Id"}}, False),
    ("paramNames empty",
     {"scheme": "apiKeyHeader", "secrets": {"token": "env://T"}, "paramNames": {}}, False),
    ("paramNames on basic",
     {"scheme": "basic", "secrets": {"username": "env://U", "password": "env://P"},
      "paramNames": {"username": "X-U"}}, False),
    ("paramNames on none",
     {"scheme": "none", "paramNames": {"token": "X-Key"}}, False),
    ("valuePrefix alongside paramNames",
     {"scheme": "apiKeyHeader", "valuePrefix": "Bearer ",
      "secrets": {"token": "env://T", "account": "env://A"},
      "paramNames": {"token": "X-Api-Key", "account": "X-Account-Id"}}, False),
]

# (pointer, value, want_valid) — a secret is always a pointer, a public key is
# always material, and neither may be written in the other's form.
K = "PublicKey/properties/key"
NARROWING = [
    ("Secret", "env://HASURA_TOKEN",              True),
    ("Secret", "inline:abc123",                   True),
    ("Secret", "base64:" + "a" * 43 + "=",        False),  # key material, not a pointer
    ("Secret", "bare-pasted-key",                 False),  # no scheme at all
    ("Secret", "env://lowercase",                 False),
    (K,        "base64:" + "a" * 43 + "=",        True),
    (K,        "env://SOME_KEY",                  False),  # a pointer cannot verify a signature
    (K,        "a" * 43 + "=",                    False),  # no scheme prefix
    (K,        "base64:" + "a" * 42 + "=",        False),  # 31 bytes, not 32
    (K,        "base64:" + "a" * 44 + "=",        False),  # 33 bytes, not 32
    (K,        "base64:" + "a" * 43 + "?",        False),  # not base64
]


# --- shape: what `type` decides. Each false case is a record that a reader would
# --- call plausible and the schema must refuse.
NODE = {"participantId": "provider-network-vistaar.da.gov.in", "name": "P",
        "type": "node", "status": "active",
        "baseUrl": "https://provider-network-vistaar.da.gov.in/beckn", "role": "BPP",
        "keys": [{"keyId": "k1", "use": "sign", "alg": "ed25519",
                  "key": "base64:xq4+2oQ6MgSZdHHBMtNd1TmnPTmzY5UoZlqzf0yn6ZA=",
                  "validFrom": "2026-08-01T00:00:00Z", "status": "active"}]}
UP = {"participantId": "mausamgram", "name": "U", "type": "upstream", "status": "active",
      "baseUrl": "https://mausamgram.imd.gov.in/nwpapi",
      "auth": {"scheme": "basic", "secrets": {"username": "env://U", "password": "env://P"}}}


def var(base, **kw):
    """A copy of base with kw applied; a None value deletes the field."""
    r = json.loads(json.dumps(base))
    for k, v in kw.items():
        r.pop(k, None) if v is None else r.__setitem__(k, v)
    return r


SHAPE = [
    ("node, hostname id over https",        NODE, True),
    ("upstream, credential over https",     UP, True),
    ("upstream, scheme none over http",     var(UP, participantId="oan-vector",
                                                baseUrl="http://3.6.146.174:8882",
                                                auth={"scheme": "none"}), True),
    # a node's id IS its wire identity, so it must be a hostname
    ("node id that is not a hostname",      var(NODE, participantId="oan-provider"), False),
    # each half refuses the other's fields
    ("node carrying auth",                  var(NODE, auth={"scheme": "none"}), False),
    ("node without role",                   var(NODE, role=None), False),
    ("node without keys",                   var(NODE, keys=None), False),
    ("upstream carrying role",              var(UP, role="BPP"), False),
    ("upstream carrying keys",              var(UP, keys=NODE["keys"]), False),
    ("upstream without auth",               var(UP, auth=None), False),
    # transport
    ("node over plaintext http",            var(NODE, baseUrl="http://p.da.gov.in/beckn"), False),
    ("credential over plaintext http",      var(UP, baseUrl="http://mausamgram.imd.gov.in/x"), False),
    # the discriminator itself
    ("no type at all",                      var(UP, type=None), False),
    ("a type value nobody defined",         var(NODE, type="external"), False),
    ("no baseUrl",                          var(UP, baseUrl=None), False),
    # guards against a revert to the nested shape
    ("subscriberId resurrected",            var(NODE, subscriberId="x.da.gov.in"), False),
    ("node wrapper resurrected",            var(NODE, node={"role": "BPP"}), False),
    ("upstream wrapper resurrected",        var(UP, upstream={"baseUrl": "https://a.b/c"}), False),
]


def main():
    import jsonschema
    sch = json.load(io.open("schemas/Participant.json"))
    V = jsonschema.Draft7Validator(sch)
    bad = 0
    for name, inst, want in SHAPE:
        got = not list(V.iter_errors({"Participant": inst}))
        hit = got == want
        bad += not hit
        print(f"  {'PASS' if hit else 'FAIL'}  {name:52} valid={str(got):5} want={want}")
    print(f"\nshape cases: {len(SHAPE)} run, {bad} failing\n")

    for name, auth, want in CASES:
        got = not list(V.iter_errors(rec(auth)))
        hit = got == want
        bad += not hit
        print(f"  {'PASS' if hit else 'FAIL'}  {name:52} valid={str(got):5} want={want}")
    print(f"\nauth cases: {len(CASES)} run\n")

    for defn, value, want in NARROWING:
        V = jsonschema.Draft7Validator({**sch, "$ref": f"#/definitions/{defn}"})
        label = defn.split("/")[0]
        got = not list(V.iter_errors(value))
        hit = got == want
        bad += not hit
        shown = value if len(value) <= 26 else value[:23] + "..."
        print(f"  {'PASS' if hit else 'FAIL'}  {label:10} {shown:30} valid={str(got):5} want={want}")
    print(f"\nnarrowing cases: {len(NARROWING)} run")
    print(f"total failing: {bad}")
    return 1 if bad else 0

if __name__ == "__main__":
    sys.exit(main())
