# imd-city-weather — city forecast

**Shape: enriched.** Same single call as [Mausamgram](mausamgram.md), but the Beckn body
alone cannot address the upstream: IMD's city endpoint takes a **station**, and the
farmer's app sends a **point**. An `enricher` closes that gap before the request mapping
runs.

*[Use cases](README.md) · [Registry schema](../02-registry-schema.md) · [Overview](../01-overview.md) · [docs home](../README.md)*

| | |
|---|---|
| Provider | `imd-city-weather` — IMD City Weather |
| Capability | `openagrinet:WeatherObservation` |
| Action | `select` |
| Auth | none |
| Enricher | `nearestStation` — **object form**, carries its own config and DSN |
| Mappings | `registry/mappings/imd-city-weather/select.{request,response}.jsonata` |

---

## What is different

The enricher takes the **object form** — the only binding in the registry that does. Two
things follow:

1. **Its Postgres DSN is an `env://` pointer in the registry**, not a `process.env` read
   inside the plugin. The registry stays the single source of truth for every address the
   adapter dials — a plugin that reaches for the environment on its own puts one of those
   addresses somewhere nobody is auditing.
2. **`maxStationAttempts` bounds a fan-out that is otherwise invisible.** The lookup walks
   outward from the point until it finds a station carrying data. Left in code, *how many
   probes is this allowed to make?* is answerable only by reading the plugin; in the
   registry it is a number an operator can change.

What the registry does **not** hold is the behaviour: it names `nearestStation` and
bounds it, and the geo query itself is Go.

## Flow

```
select ─▶ resolve binding ─▶ enrich ─────────────▶ map request ─▶ GET ─▶ map response
          one exact-match    nearestStation:       station id     IMD    on_select:
          read on bindingKey point → station                             WeatherObservation[]
```

Identical to the Mausamgram trace in every respect except the enrich box — read
[that page](mausamgram.md) for the full payloads.

---

## The registry records

```json
{
  "Provider": {
    "providerId": "imd-city-weather",
    "name": "IMD City Weather",
    "baseUrl": "https://city.imd.gov.in",
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
    "bindingKey": "imd-city-weather|openagrinet:WeatherObservation|select",
    "providerId": "imd-city-weather",
    "capabilityCode": "openagrinet:WeatherObservation",
    "action": "select",
    "method": "GET",
    "path": "/citywx/city_weather_test.php",
    "requestMapping": "mappings/imd-city-weather/select.request.jsonata",
    "responseMapping": "mappings/imd-city-weather/select.response.jsonata",
    "enricher": {
      "name": "nearestStation",
      "config": {
        "maxDistanceKm": 50,
        "maxStationAttempts": 5
      },
      "secrets": {
        "dsn": "env://IMD_DB_DSN"
      }
    },
    "timeoutMs": 15000,
    "retryMax": 1,
    "status": "active"
  }
}
```
