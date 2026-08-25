# ADR-0009 — Four-layer configuration, environment on top

**Status:** Accepted
**Date:** 2026-08-25

## Context

TRD §1 requires configurability without a redeploy; TRD §8 requires that
secrets come from a secret store. Those two pull in opposite directions if
configuration is a single file.

## Decision

`caarlos0/env/v11` plus `yaml.v3`, four layers, lowest precedence first:

1. `envDefault` struct tags — the floor
2. `config/common.yaml` — reviewed repository defaults
3. `config/instance.yaml` — deployment-local, optional, gitignored
4. process environment — always wins

A YAML key matching no `Config` field fails startup; validation failures fail
startup. Neither file may contain a secret.

## Alternatives considered

- **viper** — layering two YAML documents under `env.Parse` is a function.
  viper is a dependency with its own precedence model, its own key-casing rules
  and its own answer to unknown keys.

## Consequences

The environment beats a checked-out path, which is what lets `DATABASE_URL`
come from a secret store. A typo in either YAML file fails the boot rather than
silently doing nothing — the failure mode a warn-and-continue loader has, and
the reason unknown keys are an error here.
