# pmfby — crop insurance

**Shape: gated multi-step.** The hardest provider in Bharat Vistaar — three Beckn actions,
six upstream calls, two authentication schemes, and a proof that has to survive *between*
requests. It needs **no bespoke code**.

*[Use cases](README.md) · [Registry schema](../02-registry-schema.md) · [Overview](../01-overview.md) · [docs home](../README.md)*

| | |
|---|---|
| Provider | `pmfby` — Pradhan Mantri Fasal Bima Yojana |
| Capability | `openagrinet:InsurancePolicy` (`init`, `status`) · `openagrinet:InsuranceClaim` (`discover`) |
| Actions | `init` (single) · `status` (`steps[3]`, **grants**) · `discover` (`steps[2]`, **gated**) |
| Auth | `loginToken`, plus a `staticToken` profile for one endpoint |
| Session | scope `otpVerified`, 900s, in ONIX's `cache` plugin |
| Mappings | `registry/mappings/pmfby/*.jsonata` |

---

## What is different

**The OTP arrives out of band, so the flow cannot be one request.** PMFBY texts a code by
SMS; the farmer reads it and types it back. That human pause is why this is three Beckn
actions rather than one long `steps[]` — and all three carry the same `transaction_id`,
which is the whole mechanism the session fields hang on.

**The grant sits on the step, the gate on the binding.** `verifyMobile` is step 1 of
`status`; when that call succeeds, scope `otpVerified` is recorded against the transaction
for 900 seconds. `discover` then refuses before making *any* upstream call unless that
grant is still live. Why each sits where it does — and why the two are not symmetric — is
argued in [Registry schema](../02-registry-schema.md#schema).

**Two auth schemes, one provider.** Step `farmer` names `authProfile: staticToken` because
`farmerMobileExists` takes a query token, while every other endpoint takes the login token
plus a static `password` header. `auth` stays the default; the profile is the exception.

**`farmerID` threads by JSONata**, from the `farmer` step into the `policy` step — no code,
just `steps.farmer` in the next step's mapping.

## Flow

```
init     ─▶ POST /nic/getOtp                      ─▶ on_init   (PMFBY sends the SMS)

            ···  farmer reads the SMS  ···

status   ─▶ step 1  POST /nic/verifyMobile    GRANT otpVerified, 900s
            step 2  GET  /farmerMobileExists  (authProfile: staticToken)
            step 3  GET  /farmerpolicylist    (reads steps.farmer)
                                                  ─▶ on_status

discover ─▶ GATE otpVerified — checked BEFORE any upstream call
            step 1  GET  /farmerMobileExists  (authProfile: staticToken)
            step 2  GET  /claims/claimSearchReport
                                                  ─▶ on_discover
```

One transaction, three actions, six upstream calls after the login. The grant lives in
ONIX's existing `cache` plugin under `session:{scope}:{transaction_id}`, written by the
generic executor — never by a provider plugin.

---

## The registry records

```json
{
  "Provider": {
    "providerId": "pmfby",
    "name": "Pradhan Mantri Fasal Bima Yojana",
    "baseUrl": "https://pmfby.example.gov.in",
    "status": "active",
    "auth": {
      "scheme": "loginToken",
      "paramName": "token",
      "extraHeaders": {
        "password": "env://PMFBY_OTP_PASSWORD"
      },
      "secrets": {
        "mobile": "env://PMFBY_MOBILE",
        "password": "env://PMFBY_PASSWORD"
      },
      "login": {
        "path": "/api/v2/external/service/login",
        "method": "POST",
        "tokenPath": "data.data.token",
        "ttlSeconds": 900,
        "bodyMapping": "{ \"deviceType\": \"web\", \"mobile\": mobile, \"otp\": 123456, \"password\": password }"
      }
    },
    "authProfiles": {
      "staticToken": {
        "scheme": "apiKeyQuery",
        "paramName": "authToken",
        "secrets": {
          "apiKey": "env://PMFBY_AUTH_TOKEN"
        }
      }
    }
  }
}
```

```json
{
  "ProviderCapability": {
    "bindingKey": "pmfby|openagrinet:InsurancePolicy|init",
    "providerId": "pmfby",
    "capabilityCode": "openagrinet:InsurancePolicy",
    "action": "init",
    "method": "POST",
    "path": "/api/v1/services/nic/getOtp",
    "requestMapping": "mappings/pmfby/init.request.jsonata",
    "responseMapping": "mappings/pmfby/init.response.jsonata",
    "timeoutMs": 20000,
    "status": "active"
  }
}
```

```json
{
  "ProviderCapability": {
    "bindingKey": "pmfby|openagrinet:InsurancePolicy|status",
    "providerId": "pmfby",
    "capabilityCode": "openagrinet:InsurancePolicy",
    "action": "status",
    "status": "active",
    "timeoutMs": 20000,
    "steps": [
      {
        "id": "verifyMobile",
        "method": "POST",
        "path": "/api/v1/services/nic/verifyMobile",
        "requestMapping": "mappings/pmfby/status.verify-mobile.request.jsonata",
        "sessionGrant": { "scope": "otpVerified", "ttlSeconds": 900 }
      },
      {
        "id": "farmer",
        "method": "GET",
        "path": "/api/v1/services/services/farmerMobileExists",
        "authProfile": "staticToken",
        "requestMapping": "mappings/pmfby/status.farmer.request.jsonata"
      },
      {
        "id": "policy",
        "method": "GET",
        "path": "/api/v1/policy/policy/farmerpolicylist",
        "requestMapping": "mappings/pmfby/status.policy.request.jsonata"
      }
    ],
    "responseMapping": "mappings/pmfby/status.response.jsonata"
  }
}
```

```json
{
  "ProviderCapability": {
    "bindingKey": "pmfby|openagrinet:InsuranceClaim|discover",
    "providerId": "pmfby",
    "capabilityCode": "openagrinet:InsuranceClaim",
    "action": "discover",
    "sessionGate": { "scope": "otpVerified" },
    "status": "active",
    "timeoutMs": 20000,
    "steps": [
      {
        "id": "farmer",
        "method": "GET",
        "path": "/api/v1/services/services/farmerMobileExists",
        "authProfile": "staticToken",
        "requestMapping": "mappings/pmfby/discover.farmer.request.jsonata"
      },
      {
        "id": "claim",
        "method": "GET",
        "path": "/api/v1/claims/claims/claimSearchReport",
        "requestMapping": "mappings/pmfby/discover.claim.request.jsonata"
      }
    ],
    "responseMapping": "mappings/pmfby/discover.response.jsonata"
  }
}
```
