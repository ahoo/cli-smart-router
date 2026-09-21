# ADR 0008: Configurable Classifier Requests, Robust Verdict Parsing, Safe Telemetry

## Status

Accepted.

## Context

The classifier path had four gaps. The token budget was hardcoded
(`DefaultClassifierMaxTokens = 500`), so reasoning-style classifiers could
truncate the verdict after a long thinking trace. Per-model entries carried
only `provider`/`model`/`headers`, with no way to pass provider-specific
request options (for example Qwen/SiliconFlow parameters). The prompt
requested `confidence` and `reason`, inviting prose that complicated parsing.
The parser read only the first balanced JSON object from a single preferred
field, so a valid verdict in a later field or object was missed. The trace
logged the raw classifier response and error text, and `classifier.timeout`
was parsed but could never fire because `host.model.execute` is synchronous
and non-cancellable.

## Decision

- Add `classifier.max_tokens`, defaulting to `500` for compatibility when
  unset or `<= 0`. The current intended deployment uses `1000`.
- Add `classifier.models[].request_overrides`, a generic YAML map merged into
  the OpenAI request body as passthrough (no provider support is claimed).
  Invariants are reapplied after the merge, so `model`, `messages`, `stream`,
  `temperature`, and `max_tokens` always win. Per-model `headers` continue to
  be forwarded verbatim.
- Narrow the prompt to `{"selected_model":"<exact-id>"}` only: it marks the
  catalog and user text as untrusted data, requests no reason or confidence,
  and asks for the verdict first with no preamble. Extra legacy fields are
  still accepted; only `selected_model` is read.
- Parse verdicts by scanning `content`, `reasoning_content`, `reasoning`,
  then the raw body when appropriate, extracting every balanced JSON object
  per source. The first object naming an exact configured model whose
  provider is available wins; unknown selections are skipped and unavailable
  ones are recorded separately (`invalid_verdict` / `unavailable_selection`).
  Deterministic fallback is unchanged.
- Replace raw trace content with structured attempts: per-attempt outcome,
  HTTP status, latency, verdict source, and selected model, plus attempt
  count and total latency. The trace `error` carries only a coarse category:
  `no_models`, the last attempt's outcome, or `attempts_exhausted` when no
  usable non-empty attempt exists. A successful route uses
  `Reason: "classifier:<classifier-model-id>"`.
- Add `virtual_model_status` to the management status response: per-entry
  effective classifier summary (enabled, models, max attempts, max tokens),
  excluding headers and overrides.
- Keep `classifier.timeout` parsed for forward compatibility but document it
  as unenforceable: no deadline can be applied to the synchronous host call.
  Budget control is via `max_tokens` and `max_attempts`.

## Consequences

- Deployments can size the classifier budget and pass provider-specific
  options without code changes or provider-specific claims.
- Reasoning-style classifiers that think before answering are parsed
  reliably, and prompt-injection text in the catalog or request cannot change
  the output contract.
- Debug logs and status output carry no raw classifier responses, error
  text, headers, or overrides.
- Operators must not rely on `classifier.timeout` to bound latency.
- The decision-log classifier trace schema and successful route reason are intentionally breaking telemetry changes: consumers must migrate from the old `model`/`response` fields and classifier-generated reason to structured `attempts` and `classifier:<classifier-model-id>`.
