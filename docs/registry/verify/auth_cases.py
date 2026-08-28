"""Auth and material-reference cases for schemas/Participant.json.

Run:  python3 verify/auth_cases.py          (from docs/registry)
Needs: jsonschema

Each case is (name, auth, want_valid). A case that passes for the wrong reason is
worse than no case, so the multi-param cases assert both halves: the well-formed
record is accepted AND every malformed neighbour is refused.

NARROWING pins the second half: `Secret` is `MaterialRef` and must stay a
pointer. Without these, widening MaterialRef to add `vault://` would silently
make a bare pasted credential legal.
"""
import json, io, sys

def rec(auth):
    return {"Participant": {"participantId": "probe-x", "name": "Probe", "status": "active",
                            "upstream": {"baseUrl": "https://a.example/v1", "auth": auth}}}

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


def main():
    import jsonschema
    sch = json.load(io.open("schemas/Participant.json"))
    V = jsonschema.Draft7Validator(sch)
    bad = 0
    for name, auth, want in CASES:
        got = not list(V.iter_errors(rec(auth)))
        hit = got == want
        bad += not hit
        print(f"  {'PASS' if hit else 'FAIL'}  {name:52} valid={str(got):5} want={want}")
    print(f"\nauth cases: {len(CASES)} run, {bad} failing\n")

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
