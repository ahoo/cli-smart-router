# ADR 0007: Multiple Independently Routable Virtual Models

## Status

Accepted.

## Context

The plugin exposed exactly one virtual model (`virtual_model: router:auto`).
Users with different routing/model combinations (e.g. a cheap lane, a security
lane) had to copy the whole plugin binary once per lane, because the config,
the `model.register` output, and the `model.route` match were all singletons.

## Decision

- Add `virtual_models:`, a list of named entries. Each entry carries its own
  `strategy`, `preference`, `models`, `routes`, `classifier`, `cache`, and
  `routing`. Entries never inherit routing fields from the top level; unset
  entry fields fall back to code defaults (the same defaults `DefaultConfig`
  applies to the legacy top-level fields). This keeps every entry
  self-contained and avoids surprising cross-entry coupling.
- Keep the legacy `virtual_model` plus top-level routing fields working: when
  `virtual_models` is absent, they form the single entry (backward compatible).
- `model.register` returns one model per entry; `model.route` resolves the
  requested name to its entry (`Config.WithEntry`) and runs the whole existing
  pipeline (session pin -> cache -> strategy -> fallback) on the entry-scoped
  config. Unknown names return `Handled: false`.
- Session pins, route cache keys (already keyed by requested model), and
  Decision Engine fallback chains are namespaced per virtual model, so one
  session can pin different models per lane. Pre-upgrade bare session keys are
  still read once for continuity; new pins always use namespaced keys.
- Catalog refresh stays plugin-wide and excludes every virtual model name.
- The status endpoint keeps `virtual_model` (legacy) and adds
  `virtual_models` (full list).

## Consequences

- One plugin instance serves N lanes; no binary copies needed.
- Duplicate entry names are deduplicated (first wins); nameless entries are
  dropped during normalization.
- `executor_fallback` chains resolve per requested entry; callers should pass
  the virtual model name as the executor request model.
