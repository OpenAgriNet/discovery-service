"""Auth cases for schemas/Provider.json.

Run:  python3 verify/auth_cases.py          (from docs/registry)
Needs: jsonschema

Each case is (name, auth, want_valid). A case that passes for the wrong reason is
worse than no case, so the multi-param cases assert both halves: the well-formed
record is accepted AND every malformed neighbour is refused.
"""
import json, io, sys

def rec(auth):
    return {"Provider": {"providerId": "probe-x", "name": "Probe",
                         "baseUrl": "https://a.example/v1", "status": "active", "auth": auth}}

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

def main():
    import jsonschema
    V = jsonschema.Draft7Validator(json.load(io.open("schemas/Provider.json")))
    bad = 0
    for name, auth, want in CASES:
        got = not list(V.iter_errors(rec(auth)))
        hit = got == want
        bad += not hit
        print(f"  {'PASS' if hit else 'FAIL'}  {name:52} valid={str(got):5} want={want}")
    print(f"\nauth cases: {len(CASES)} run, {bad} failing")
    return 1 if bad else 0

if __name__ == "__main__":
    sys.exit(main())
