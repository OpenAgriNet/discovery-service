# OpenAgriNet registry

Three tables that tell an adapter who is on the network, what data types exist, and how to
call each provider.

## Deployment topology

Three ONIX adapters, one registry, one discovery-service, on the **Bharat Vistaar** subnet
(`networkId: da.gov.in/vistaar`).

| adapter | `participantId` | sits | does |
|---|---|---|---|
| **consumer node** | `seeker-network-vistaar.da.gov.in` | experience layer, beside the farmer app | signs `discover` / `select` |
| **network node** | `discovery-network-vistaar.da.gov.in` | network, alongside discovery-service | exposes publish + discover; answers `discover` from published catalogs |
| **provider node** | `provider-network-vistaar.da.gov.in` | provider side | terminates `select`, calls the external provider API, signs `on_select` |

**Every transaction is synchronous** — `on_discover` and `on_select` are response bodies, not
inbound calls. `beckn.yaml` v2.0.0 still says the outcome MUST arrive by callback and is to be
amended; until it is, this is a deviation on a MUST.
[usecases.md](usecases.md#both-hops-are-synchronous--the-spec-has-not-caught-up) states what it
costs.

Signature verification happens in ONIX, not in discovery-service
(`AUTH_ENABLE_SIGNATURE_VERIFICATION=false`, and `true` refuses to boot). Discovery-service must
therefore have no route from outside — `context.bapId` is only trustworthy downstream of the
verifier.

## One flow — weather forecast

```
  farmer
    │
    ▼
┌──────────────┐   discover   ┌──────────────┐
│ consumer     │─────────────▶│ network node │──▶ discovery-service
│ node   (BAP) │◀─────────────│  (NETWORK)   │    answers from the published catalog
└──────────────┘  on_discover └──────────────┘
    │
    │  select   provider = mausamgram
    ▼
┌──────────────┐
│ provider     │   GET https://mausamgram.imd.gov.in/nwpapi/get-daily
│ node   (BPP) │──────────────────────────────────▶  IMD Mausamgram NWP
└──────────────┘                                     (an ordinary HTTP API)
    │
    │  on_select   the typed forecast
    ▼
  consumer node
```

Four registry records carry that flow: the three nodes, plus `mausamgram` as an upstream and its
binding to `openagrinet:WeatherObservation`.

## Documents

| | |
|---|---|
| [schemas.md](schemas.md) | the three entities, field by field |
| [examples.md](examples.md) | every record to seed |
| [api.md](api.md) | the registry's own REST surface |
| [usecases.md](usecases.md) | six farmer questions, and the weather one in full |

[`schemas/`](schemas) holds the draft-07 files — **those are the contract**, this folder
describes them. [`verify/`](verify) keeps these pages true. [`archive/`](archive) is the BV Beckn
adapter's design set, a different system, kept verbatim for interop context.
