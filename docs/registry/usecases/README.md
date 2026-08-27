# Use cases — every Bharat Vistaar provider

All eight providers from `../../PROVIDERS.md`, expressed as registry records. Every block on
these pages is generated from records validated against `registry/schemas/`, and the
referential-integrity pass in [Conformance](../reference/conformance.md) re-parses them
out of the markdown — the docs and the registry cannot drift.

*[Overview](../01-overview.md) · [Registry schema](../02-registry-schema.md) · [Adding a provider](../03-adding-a-provider.md) · [docs home](../README.md)*

---

## Four shapes

Eleven bindings across eight providers, but only **four call shapes**. Read these four and
you have read them all; everything below is a variation on one of them.

| Shape | Read this | What it introduces |
|---|---|---|
| simple | **[mausamgram](mausamgram.md)** | one action, one call — traced end to end with real payloads |
| enriched | **[imd-city-weather](imd-city-weather.md)** | `enricher` object form: config and an `env://` DSN in the registry |
| multi-step | **[pm-kisan](pm-kisan.md)** | `steps[]`, later steps reading `steps.<id>` |
| gated multi-step | **[pmfby](pmfby.md)** | `sessionGate` / `sessionGrant` across three actions |

## All eleven bindings

| Provider | Capability | Action | Shape | Where |
|---|---|---|---|---|
| `mausamgram` | `WeatherObservation` | `select` | simple | [page](mausamgram.md) |
| `imd-city-weather` | `WeatherObservation` | `select` | enriched | [page](imd-city-weather.md) |
| `agmarknet` | `MandiPriceObservation` | `select` | enriched | [below](#agmarknet--mandi-price-discovery) |
| `oan-vector` | `KnowledgeResource` | `select` | enriched | [below](#oan-vector--knowledge-advisory) |
| `hasura-content` | `KnowledgeResource` | `select` | enriched, GraphQL | [below](#hasura-content--scheme--icar-content) |
| `soil-health-card` | `SoilHealthReport` | `status` | simple, refresh→bearer | [below](#soil-health-card--soil-test-reports) |
| `pm-kisan` | `BeneficiaryStatus` | `init` | simple | [page](pm-kisan.md) |
| `pm-kisan` | `BeneficiaryStatus` | `status` | multi-step `[2]` | [page](pm-kisan.md) |
| `pmfby` | `InsurancePolicy` | `init` | simple | [page](pmfby.md) |
| `pmfby` | `InsurancePolicy` | `status` | multi-step `[3]`, grants | [page](pmfby.md) |
| `pmfby` | `InsuranceClaim` | `discover` | multi-step `[2]`, gated | [page](pmfby.md) |

Nine are single-call; two need `steps[]`. Note that **no BV binding serves
`WeatherAdvisory`** — network-specs defines that pack (and its advertisement pack, still
named `WeatherAdvisoryCapability` at `c56ee68`), but both weather providers report
observations and forecasts, which is `WeatherObservation`.

---

## Remaining providers

The four below add no new call shape, so they are records and notes rather than full
walkthroughs. If one of them starts behaving differently from its shape's exemplar, that
is the moment it earns its own page.

### agmarknet — Mandi price discovery

`enricher` resolves lat/lon to state, district, market and commodity codes before the
mapping runs. The production endpoint takes lat/lon directly
(`fetch-agmarknet-vistaar-location`); this binding is pinned to the branch variant the
seed was built from.

```json
{
  "Provider": {
    "providerId": "agmarknet",
    "name": "Agmarknet Vistaar (Directorate of Marketing & Inspection)",
    "baseUrl": "https://api.agmarknet.gov.in",
    "status": "active",
    "auth": {
      "scheme": "apiKeyQuery",
      "paramName": "token",
      "secrets": {
        "token": "env://MANDI_TOKEN"
      }
    }
  }
}
```

```json
{
  "ProviderCapability": {
    "bindingKey": "agmarknet|openagrinet:MandiPriceObservation|select",
    "providerId": "agmarknet",
    "capabilityCode": "openagrinet:MandiPriceObservation",
    "action": "select",
    "method": "POST",
    "path": "/v1/fetch-agmarknet-vistaar",
    "requestMapping": "mappings/agmarknet/select.request.jsonata",
    "responseMapping": "mappings/agmarknet/select.response.jsonata",
    "enricher": "marketAndCommodityCodes",
    "timeoutMs": 20000,
    "retryMax": 2,
    "status": "active"
  }
}
```

---

### oan-vector — Knowledge advisory

Search tuning is exposed to the caller through Beckn tags, so `enricher` normalises them
before they reach the payload. Today the upstream is a hardcoded bare IP over plain HTTP
(`app.service.ts:456`) - moving it into `baseUrl` is the point of the exercise.

```json
{
  "Provider": {
    "providerId": "oan-vector",
    "name": "OAN Vector Index",
    "baseUrl": "http://3.6.146.174:8882",
    "status": "active",
    "auth": {
      "scheme": "none"
    }
  }
}
```

```json
{
  "ProviderCapability": {
    "bindingKey": "oan-vector|openagrinet:KnowledgeResource|select",
    "providerId": "oan-vector",
    "capabilityCode": "openagrinet:KnowledgeResource",
    "action": "select",
    "method": "POST",
    "path": "/indexes/oan-index/search",
    "requestMapping": "mappings/oan-vector/select.request.jsonata",
    "responseMapping": "mappings/oan-vector/select.response.jsonata",
    "enricher": "knowledgeQueryParams",
    "timeoutMs": 15000,
    "status": "active"
  }
}
```

---

### hasura-content — Scheme + ICAR content

GraphQL: `path` is constant and the operation lives in the query document, so `method` and
`path` stop discriminating. The current code builds this `where` clause by **string
concatenation from network input** (`app.service.ts:333-347`); a parameterised `variables`
block is what closes that.

```json
{
  "Provider": {
    "providerId": "hasura-content",
    "name": "Vistaar Knowledge Content (Hasura)",
    "baseUrl": "https://content.internal",
    "status": "active",
    "auth": {
      "scheme": "apiKeyHeader",
      "paramName": "x-hasura-admin-secret",
      "secrets": {
        "adminSecret": "env://HASURA_GRAPHQL_ADMIN_SECRET"
      }
    }
  }
}
```

```json
{
  "ProviderCapability": {
    "bindingKey": "hasura-content|openagrinet:KnowledgeResource|select",
    "providerId": "hasura-content",
    "capabilityCode": "openagrinet:KnowledgeResource",
    "action": "select",
    "method": "POST",
    "path": "/v1/graphql",
    "requestMapping": "mappings/hasura-content/select.request.jsonata",
    "responseMapping": "mappings/hasura-content/select.response.jsonata",
    "enricher": "knowledgeQueryParams",
    "timeoutMs": 15000,
    "retryMax": 0,
    "status": "active"
  }
}
```

---

### soil-health-card — Soil test reports

The refresh-token exchange is absorbed by `Auth.login` even though it is a GraphQL POST to
the same `/graphql` path, not a REST login route - `login.path` and `login.bodyMapping`
carry it, so the binding is still a single call. The refresh token in source today is
hardcoded and **expired 2025-01-10**.

```json
{
  "Provider": {
    "providerId": "soil-health-card",
    "name": "Soil Health Card",
    "baseUrl": "https://soilhealth.dac.gov.in",
    "status": "active",
    "auth": {
      "scheme": "loginToken",
      "paramName": "Authorization",
      "secrets": {
        "refreshToken": "env://SHC_REFRESH_TOKEN"
      },
      "login": {
        "path": "/graphql",
        "method": "POST",
        "tokenPath": "data.data.generateAccessToken.token",
        "ttlSeconds": 3600,
        "bodyMapping": "{ \"query\": \"query Query($refreshToken:String!){ generateAccessToken(refreshToken:$refreshToken) }\", \"variables\": { \"refreshToken\": refreshToken } }"
      }
    }
  }
}
```

```json
{
  "ProviderCapability": {
    "bindingKey": "soil-health-card|openagrinet:SoilHealthReport|status",
    "providerId": "soil-health-card",
    "capabilityCode": "openagrinet:SoilHealthReport",
    "action": "status",
    "method": "POST",
    "path": "/graphql",
    "requestMapping": "mappings/soil-health-card/status.request.jsonata",
    "responseMapping": "mappings/soil-health-card/status.response.jsonata",
    "timeoutMs": 30000,
    "status": "active"
  }
}
```
