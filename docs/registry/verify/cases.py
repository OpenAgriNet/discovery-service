"""Shape and key-material cases for schemas/Participant.json.

Run:  python3 verify/cases.py          (from docs/registry)
Needs: jsonschema

These are the rules no record in examples.md can demonstrate, because a valid
record cannot show an illegal combination — a node without a role, an upstream
carrying keys, a node id that is not a hostname, a node over plaintext http.

SHAPE pins what `type` decides. Six of its cases guard a revert rather than a
bug. Five are refused by `additionalProperties: false` — `subscriberId`
reappearing, either wrapper object (`node` or `upstream`) coming back, and `auth`
coming back on either half — and the sixth, `keys` going back to an array, by the
type of the `keys` property itself. The last three are the shape this file was
written for; the schema now refuses them, and a case is the only thing that keeps
refusing them once nobody remembers why.

NARROWING pins the material grammar: a public key is always **material**
(`base64:` + 44 chars), and the length cases pin 32 bytes, which is what both
Ed25519 and X25519 use — so a truncated key is rejected at write time rather
than at the first signature it fails to verify. It has one half now; a secret is
not expressible in these schemas at all, and verify/records.py asserts that no
record smuggles one into a field that is.
"""
import json, io, sys

# (pointer, value, want_valid)
K = "PublicKey/properties/key"
NARROWING = [
    (K, "base64:" + "a" * 43 + "=", True),
    (K, "env://SOME_KEY",           False),  # a pointer cannot verify a signature
    (K, "inline:abc123",            False),  # nor can a credential form
    (K, "a" * 43 + "=",             False),  # no scheme prefix
    (K, "base64:" + "a" * 42 + "=", False),  # 31 bytes, not 32
    (K, "base64:" + "a" * 44 + "=", False),  # 33 bytes, not 32
    (K, "base64:" + "a" * 43 + "?", False),  # not base64
]

KEY = {"keyId": "k1", "use": "sign", "alg": "ed25519",
       "key": "base64:xq4+2oQ6MgSZdHHBMtNd1TmnPTmzY5UoZlqzf0yn6ZA=",
       "validFrom": "2026-08-01T00:00:00Z", "status": "active"}

NODE = {"participantId": "provider-network-vistaar.da.gov.in", "name": "P",
        "type": "node", "status": "active",
        "baseUrl": "https://provider-network-vistaar.da.gov.in/beckn", "role": "BPP",
        "keys": KEY}
UP = {"participantId": "mausamgram", "name": "U", "type": "upstream", "status": "active",
      "baseUrl": "https://mausamgram.imd.gov.in/nwpapi"}


def var(base, **kw):
    """A copy of base with kw applied; a None value deletes the field."""
    r = json.loads(json.dumps(base))
    for k, v in kw.items():
        r.pop(k, None) if v is None else r.__setitem__(k, v)
    return r


SHAPE = [
    ("node, hostname id over https",        NODE, True),
    ("node holding an encryption key",      var(NODE, keys={**KEY, "use": "encrypt",
                                                            "alg": "x25519"}), True),
    ("upstream, host and base path",        UP, True),
    # an upstream may be plaintext: with no auth field the schema cannot tell whether a
    # credential rides on the call, so it cannot condition the transport rule on one.
    # The guard moved to the plugin, and nothing here can assert it.
    ("upstream over plaintext http",        var(UP, participantId="oan-vector",
                                                baseUrl="http://3.6.146.174:8882"), True),
    # a node's id IS its wire identity, so it must be a hostname
    ("node id that is not a hostname",      var(NODE, participantId="oan-provider"), False),
    # each half refuses the other's fields
    ("node without role",                   var(NODE, role=None), False),
    ("node without keys",                   var(NODE, keys=None), False),
    ("upstream carrying role",              var(UP, role="BPP"), False),
    ("upstream carrying keys",              var(UP, keys=KEY), False),
    # use fixes alg, so a signing key on the encryption curve is refused
    ("sign key on the x25519 curve",        var(NODE, keys={**KEY, "alg": "x25519"}), False),
    # transport
    ("node over plaintext http",            var(NODE, baseUrl="http://p.da.gov.in/beckn"), False),
    # the discriminator itself
    ("no type at all",                      var(UP, type=None), False),
    ("a type value nobody defined",         var(NODE, type="external"), False),
    ("no baseUrl",                          var(UP, baseUrl=None), False),
    # guards against a revert
    ("subscriberId resurrected",            var(NODE, subscriberId="x.da.gov.in"), False),
    ("node wrapper resurrected",            var(NODE, node={"role": "BPP"}), False),
    ("upstream wrapper resurrected",        var(UP, upstream={"baseUrl": "https://a.b/c"}), False),
    ("auth resurrected on an upstream",     var(UP, auth={"scheme": "none"}), False),
    ("auth resurrected on a node",          var(NODE, auth={"scheme": "none"}), False),
    ("keys back to an array",               var(NODE, keys=[KEY]), False),
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
