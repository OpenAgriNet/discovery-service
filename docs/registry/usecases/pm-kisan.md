# pm-kisan — beneficiary status

**Shape: multi-step.** One Beckn action, *several* upstream calls. PM-Kisan will not
return a benefit status until an OTP has been verified, and those are two different
endpoints — so `status` carries `steps[]` instead of `method`/`path`/`requestMapping`.

*[Use cases](README.md) · [Registry schema](../02-registry-schema.md) · [Overview](../01-overview.md) · [docs home](../README.md)*

| | |
|---|---|
| Provider | `pm-kisan` — PM-KISAN Beneficiary Services |
| Capability | `openagrinet:BeneficiaryStatus` |
| Actions | `init` (single call) · `status` (`steps[2]`) |
| Auth | AES-128-CBC envelope over every body |
| Enricher | `pmKisanIdentifierType` |
| Mappings | `registry/mappings/pm-kisan/*.jsonata` |

---

## What is different

**Two bindings, two keys.** `init` requests the OTP, `status` consumes it. Same provider,
same capability — which is why `bindingKey` carries the action as a third segment. Under a
two-part key these two rows would collide on the unique index, silently.

**`steps[]` runs in order and threads state.** Each step's JSONata sees
`{beckn, _local, steps}`, so the second step reads the first's response as
`steps.verifyOtp`, and the single `responseMapping` sees both. Any step failing NACKs the
whole action — there are no partial results.

**The envelope is not a step.** AES-128-CBC wraps *every* body, so it is declared once on
the Provider and applied by a codec hook. A step is an upstream *call*; encryption is a
property of how this provider is spoken to at all.

**No session state.** Unlike [PMFBY](pmfby.md), PM-Kisan re-proves the OTP inside the same
action — `verifyOtp` is step 1 of `status`, not a previous request. Nothing has to survive
between Beckn calls, so this binding carries no `sessionGrant` or `sessionGate`.

## Flow

```
init   ─▶ POST /ChatbotOTP                       ─▶ on_init   (PM-Kisan texts the farmer)

          ···  farmer reads the SMS  ···

status ─▶ step 1  POST /ChatbotOTPVerified          ─┐
          step 2  POST /ChatbotBeneficiaryStatus     ├─▶ one responseMapping ─▶ on_status
                  (reads steps.verifyOtp)           ─┘
```

The executor walks that array and knows nothing about PM-Kisan. The array is data; the
same loop runs every binding.

---

## The registry records

```json
{
  "Provider": {
    "providerId": "pm-kisan",
    "name": "PM-KISAN Beneficiary Services",
    "baseUrl": "https://exlink.pmkisan.gov.in",
    "status": "active",
    "auth": {
      "scheme": "encryptedEnvelope",
      "secrets": {
        "key": "env://PM_KISSAN_TOKEN"
      },
      "envelope": {
        "algorithm": "aes-128-cbc"
      }
    }
  }
}
```

```json
{
  "ProviderCapability": {
    "bindingKey": "pm-kisan|openagrinet:BeneficiaryStatus|init",
    "providerId": "pm-kisan",
    "capabilityCode": "openagrinet:BeneficiaryStatus",
    "action": "init",
    "method": "POST",
    "path": "/ChatbotOTP",
    "requestMapping": "mappings/pm-kisan/init.request.jsonata",
    "responseMapping": "mappings/pm-kisan/init.response.jsonata",
    "enricher": "pmKisanIdentifierType",
    "status": "active"
  }
}
```

```json
{
  "ProviderCapability": {
    "bindingKey": "pm-kisan|openagrinet:BeneficiaryStatus|status",
    "providerId": "pm-kisan",
    "capabilityCode": "openagrinet:BeneficiaryStatus",
    "action": "status",
    "status": "active",
    "enricher": "pmKisanIdentifierType",
    "steps": [
      {
        "id": "verifyOtp",
        "method": "POST",
        "path": "/ChatbotOTPVerified",
        "requestMapping": "mappings/pm-kisan/status.verify-otp.request.jsonata"
      },
      {
        "id": "benefit",
        "method": "POST",
        "path": "/ChatbotBeneficiaryStatus",
        "requestMapping": "mappings/pm-kisan/status.benefit.request.jsonata"
      }
    ],
    "responseMapping": "mappings/pm-kisan/status.response.jsonata"
  }
}
```
