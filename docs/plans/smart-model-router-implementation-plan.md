# Smart Model Router Implementation Plan

Live tracker for router implementation milestones. Each milestone keeps its
detailed rationale in `docs/adr/`.

## Completed

- Core routing: virtual model registration, provider routing, deterministic
  fallback (`docs/adr/0001-defer-advanced-routing-features.md`,
  `docs/adr/0002-routing-determinism-preferences-and-cache.md`).
- Local-first hybrid routing and tiered cost signals (`docs/adr/0003-*`,
  `docs/adr/0004-*`).
- Declarative Decision Engine pipeline (`docs/adr/0005-*`).
- Subagent task overrides (`docs/adr/0006-*`).
- Multiple independently routable virtual models (`docs/adr/0007-*`).
- Configurable classifier requests (`docs/adr/0008-*`):
  - `classifier.max_tokens` (default `500`; deployment uses `1000`).
  - Per-model `headers` forwarding and generic `request_overrides`
    passthrough under routing invariants
    (`model`/`messages`/`stream`/`temperature`/`max_tokens` always win).
  - Minimal verdict prompt (`{"selected_model":"<exact-id>"}` only, catalog
    and user text treated as untrusted, legacy extra fields accepted).
  - Robust parsing across `content`/`reasoning_content`/`reasoning`/raw body
    and multiple JSON objects; first exact configured-and-available model
    wins; deterministic fallback unchanged.
  - Structured attempt telemetry (outcome, status, latency, source, selected
    model, totals); no raw responses or error text in logs.
  - Per-virtual-model classifier summary on the status endpoint (headers and
    overrides excluded).
  - `classifier.timeout` documented as parsed but unenforceable (synchronous,
    non-cancellable host call).

## Open

- Streaming executor fallback (not implemented; streaming uses provider
  routing).
- Benchmark scoring beyond current capability-equivalent behavior.
- Latency-aware scoring and hard cost ceilings (reserved policy metadata).
