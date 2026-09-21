# Smart Model Router

`smart-model-router` is a native CLIProxyAPI plugin that registers configurable virtual models and routes requests for those models to real built-in provider/models.

The default virtual model is `router:auto`, but names are configurable through `virtual_model` (single) or `virtual_models` (multiple independently routable entries, e.g. `router-cheap`, `router-security`). See `configs/smart-model-router_multi.yaml` and `docs/adr/0007-multi-virtual-models.md`.

## Current Scope

This repository currently implements the router core plus the advanced features from the implementation plan:

- Native CLIProxyAPI C ABI entrypoint.
- `plugin.register` / `plugin.reconfigure` lifecycle.
- `model.register` virtual model registration.
- `model.route` routing for the configured virtual model only.
- Provider filtering through `AvailableProviders`.
- `/v1/models` catalog refresh through `host.http.do` when configured.
- External pricing fetch metadata through `host.http.do` when configured.
- Deterministic route cache with last-user-message hash keys, LRU eviction, and optional TTL.
- Session affinity for stable per-session routing.
- Local capability-aware deterministic scoring computed for every strategy, gating candidates by capabilities inferred from the prompt.
- Local-first `hybrid` strategy: skips the classifier when the local decision is confident, calls the classifier only when it is not.
- Optional LLM classifier routing through `host.model.execute` with isolated routing prompts.
- Cost/balanced/quality routing preference through `preference`.
- Optional same-request non-streaming fallback through plugin executor mode.
- `usage.handle` counters and persisted non-sensitive runtime state.
- Authenticated Management API status route.
- DDD-inspired package layout.

## Architecture

The code follows a small DDD-inspired structure:

```text
cmd/plugin/
  main.go                  # Native plugin ABI and method dispatch
internal/domain/
  config.go                # Config entities and normalization
  model.go                 # Candidate and decision types
  policy.go                # Capability-aware candidate scoring and selection
  capability.go            # Prompt -> capability tag inference
internal/application/
  router.go                # Routing use case
  registrar.go             # Virtual model registration use case
  usage.go                 # Usage counters
internal/infrastructure/
  cliproxy.go              # Minimal CLIProxyAPI JSON contracts used by V1
  rpc.go                   # RPC envelope helpers
  yaml_config.go           # config_yaml parsing
  state.go                 # In-memory config store
  runtime_state.go         # Non-sensitive runtime state, cache, catalog, pricing metadata
  prompt.go                # Shared last-user-message extraction
```

All code comments and configuration field descriptions are in English.

## Routing Behavior

The plugin only handles requests where `RequestedModel` matches `virtual_model`.

For all other models, it returns:

```json
{"Handled": false}
```

For the configured virtual model, it chooses a configured candidate whose provider is currently present in `AvailableProviders`. Configured `models` remain the authoritative source for provider, capability, cost, and quality metadata.

Decision order:

1. Existing session route, when session affinity is enabled.
2. Existing cache route, when cache is enabled and a valid entry exists.
3. Local capability-aware deterministic decision, computed for every strategy without any classifier call.
4. `hybrid`: use the local decision directly when it is confident (the prompt matched a capability the winning candidate declares); otherwise call the classifier. `llm`: always call the classifier. `capability`/`benchmark`: always use the local decision.
5. If the classifier is called and fails, use the local decision from step 3.

The local decision infers capability tags from the extracted last user message (for example, "resuma" implies `summarize`; "arquitetura" implies `architecture`/`planning`; "erro"/"bug" implies `coding`/`review`) and, when at least one candidate declares a matching capability, narrows selection to candidates that do. `preference` then ranks the remaining pool by cost/quality tier. See `docs/adr/0003-local-first-hybrid-routing.md` for details.

OpenCode subagents can declare a routing phase. A valid `X-Router-Task` header
(`planning`, `coding`, `review`, `testing`, `debug`, `security`,
`documentation`, or `performance`) overrides intent detection. When no
valid task header is present, the router accepts `X-Router-Agent` values
`planner`, `implementer`, and `reviewer`; it then checks matching
`[router-task: ...]` and `[router-agent: ...]` tags in the last user message.
This keeps an implementer on a code route even when its attached plan mentions
architecture or roadmap work.

Without an override, task detection runs the distilled intent student first
(local TF-IDF + logistic regression, no egress) and falls back to legacy
lexical keyword detection when the student abstains or errors. See
`docs/adr/0009-distilled-intent-model.md`.

The classifier, when called, receives an isolated routing prompt with the configured model catalog and the extracted last user message. The prompt asks only for `{"selected_model":"<exact-id>"}`, treats the catalog and user text as untrusted data, and requests no reason or confidence (legacy extra fields are still accepted). Verdict parsing scans `content`, `reasoning_content`, and `reasoning` (or the raw body when no usable OpenAI message field exists) for the first exact configured-and-available model; anything else falls back to the local deterministic decision. Per-model `headers` are forwarded verbatim and `request_overrides` supplies extra provider options, while `model`/`messages`/`stream`/`temperature`/`max_tokens` always win. See `docs/configuration.md`.

Default provider-route response:

```json
{
  "Handled": true,
  "TargetKind": "provider",
  "Target": "codex",
  "TargetModel": "gpt-5.4-mini",
  "Reason": "classifier:gpt-5.4-mini"
}
```

A `hybrid` request that skipped the classifier because the local decision was confident has a `Reason` prefixed with `local_confident`, for example `"local_confident deterministic_fallback strategy:hybrid capabilities:1/1 preference:balanced provider:codex cost:low"`.

When `executor_fallback.enabled: true` and the request is non-streaming, `model.route` returns `TargetKind: "self"`. The plugin executor then tries configured candidates in order with `host.model.execute` until one succeeds.

## Configuration Example

Ready-to-use example:

```text
configs/smart-model-router.yaml
```

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    smart-model-router:
      enabled: true
      priority: 100

      virtual_model: router:auto
      strategy: hybrid
      preference: balanced
      state_path: smart-model-router-state.json

      debug:
        enabled: true
        log_path: smart-model-router-decisions.jsonl

      catalog:
        source: cli_proxy_api
        base_url: http://127.0.0.1:8317
        api_key: ""
        refresh_interval: 10m
        include_router_model: false

      pricing:
        enabled: true
        url: https://raw.githubusercontent.com/ENTERPILOT/ai-model-price-list/refs/heads/main/sources/llm_prices_current.json
        refresh_interval: 6h

      cache:
        enabled: true
        max_entries: 1024
        ttl: 24h

      executor_fallback:
        enabled: false
        max_attempts: 3

      classifier:
        enabled: true
        models:
          - provider: codex
            model: gpt-5.4-mini
          - provider: claude
            model: claude-haiku-4-5-20251001
        timeout: 8s
        max_attempts: 2
        max_tokens: 1000

      routing:
        prefer_low_cost: true
        prefer_low_latency: false
        prefer_high_quality: true
        max_cost_per_request: 0.05
        max_input_tokens: 100000
        keep_same_model_per_session: true
        allow_fallback: true
        switch_threshold: 0.15
        benchmark_weight: 0.4
        llm_router_weight: 0.3
        capability_weight: 0.3

      models:
        - provider: codex
          model: gpt-5.4-mini
          capabilities: [classify, summarize, translate, simple_coding, fast, low_cost, routing]
          cost: low
          quality: medium
        - provider: claude
          model: claude-sonnet-5
          capabilities: [reasoning, writing, coding, architecture]
          cost: high
          quality: high
```

## Configuration Fields

| Field | Type | Description |
| --- | --- | --- |
| `virtual_model` | string | Virtual model name intercepted by the router. Defaults to `router:auto`. |
| `strategy` | enum | Routing strategy: `capability`, `benchmark`, `llm`, or `hybrid`. `llm` always tries the classifier first; `hybrid` tries the classifier only when the local capability-aware decision is not confident. |
| `preference` | enum | Decision influencer: `cost`, `balanced`, or `quality`. Defaults to `balanced`. |
| `state_path` | string | Optional local JSON file for non-sensitive runtime state. |
| `debug` | object | Optional JSONL route decision logging. |
| `catalog` | object | `/v1/models` catalog refresh settings. |
| `pricing` | object | External pricing refresh settings. |
| `cache` | object | Deterministic route cache settings: enabled flag, `max_entries`, and optional `ttl`. |
| `executor_fallback` | object | Optional non-streaming same-request fallback executor settings. |
| `classifier` | object | Ordered classifier routing settings. Classifier models are tried in order and must select configured candidates. |
| `routing` | object | Policy weights and limits. |
| `models` | array | Candidate provider/model matrix. |

Complete field documentation is available in `docs/configuration.md`.

## Strategy And Preference

Supported strategies:

- `capability`: local capability-aware deterministic decision only. No classifier call.
- `benchmark`: currently behaves like `capability`; benchmark scoring is reserved for future measured data.
- `llm`: classifier first on every request, local deterministic decision as fallback on failure.
- `hybrid`: local-first. Uses the local decision directly when it is confident, skipping the classifier; otherwise behaves like `llm` for that request.

Supported preferences:

- `cost`: bias toward the cheapest acceptable model.
- `balanced`: default balance of cost and quality.
- `quality`: bias toward higher-quality models.

`preference` ranks the local capability-gated candidate pool, biases the classifier prompt (`llm`, or `hybrid` when not confident), and applies conservative post-classifier tiebreaks among equivalent-tier models.

## Local-First Hybrid Routing

Every strategy computes a local decision from configuration alone, with no host call: it infers capability tags from the extracted last user message (see `models[].capabilities` below), narrows candidates to those declaring a matching capability when at least one does, and ranks the result by `preference`.

The decision is "confident" only when the prompt matched a known capability signal and the winning candidate declares it. `strategy: hybrid` uses this to skip the classifier entirely for prompts the configuration already answers clearly, for example:

- "resuma isso" -> a low-cost/summarize-capable model, no classifier call.
- "corrija este erro Go..." -> a coding/review-capable model, no classifier call.
- "planeje a arquitetura de..." -> an architecture/planning-capable model, no classifier call.
- "faça isso melhor" -> ambiguous, no capability matched, classifier is called.

An ambiguous prompt, or a configuration where no candidate declares a matching capability, always falls through to the classifier under `hybrid` (or is used as-is under `capability`/`benchmark`, which never call a classifier). See `docs/adr/0003-local-first-hybrid-routing.md` for the full design rationale.

## Deterministic Cache

When `cache.enabled` is true, repeated identical prompts reuse the same route decision.

Cache behavior:

- Keyed by the configured virtual model and extracted last user message.
- Raw prompts are not stored; only a hash key is persisted.
- `cache.max_entries` bounds cache size.
- LRU eviction removes the least recently used entry when full.
- `cache.ttl` optionally expires old decisions.

The first request logs `source: selected`; repeated matching prompts should log `source: cache`. A `hybrid` request that used the local confident decision instead of the classifier still logs `source: selected`, with `reason` prefixed by `local_confident`.

## Management Status

The plugin registers this management route:

```text
GET /v0/management/plugins/smart-model-router/status
```

The route is exposed by CLIProxyAPI under `/v0/management/...` and requires the management key.

The response includes runtime state snapshots for catalog, pricing, counters, cache, sessions, usage, and a per-virtual-model classifier summary (enabled flag, models, max attempts, max tokens; headers and overrides excluded). It never stores prompts, request bodies, credentials, API keys, or response bodies.

## Live Verification Script

Use the helper script to verify the plugin against a running CLIProxyAPI instance:

```bash
cp .env.example .env
python3 scripts/check_smart_model_router.py --once
```

Required `.env` variables:

```env
BASE_URL=http://localhost:8317
API_KEY="management-api-key"
API_KEY_MODELS="models-api-key"
```

Authentication rules used by the script:

- `/v0/...` management endpoints use `API_KEY`.
- `/v1/...` model endpoints use `API_KEY_MODELS`.
- Both keys are sent as `Authorization: Bearer <token>`.

Default basic verification checks:

- `GET /v0/management/plugins`
- `GET /v1/models`
- `GET /v0/management/plugins/smart-model-router/status`

Run the full verification, including a non-streaming chat completion through the virtual model and a post-chat status read:

```bash
python3 scripts/check_smart_model_router.py --once --all
```

Avoid running `--all` in a tight loop unless you intentionally want to generate repeated model requests.

Useful options:

- `--base-url <url>` overrides `BASE_URL` from `.env`.
- `--virtual-model <model>` overrides the default virtual model used by the check.
- `--verbose` prints raw JSON responses in addition to the console summary.

## Classifier Benchmark

Use `scripts/benchmark_classifier.py` before enabling an LLM classifier. It runs the fixed 20-case synthetic corpus against one or more classifiers on separate connections, with no retries, and persists only sanitized rows (case id, classifier, HTTP status, latency, outcome, verdict source, expected/selected model, booleans).

```bash
python3 scripts/benchmark_classifier.py --classifiers minicpm5 --output /tmp/minicpm5.json
```

Eligibility gates: exactly 20 calls, at least 19 transport successes, at least 19 valid configured-and-available verdicts, at least 18 exact target matches, and an all-attempt p95 latency within `--p95-limit-ms` (default 2000). The script exits non-zero when a classifier misses a gate and never deletes or rewrites a previous result file. Add `--qwen-profile` for Qwen-specific provider options; MiMo requests automatically carry their required session header. It never sends a classifier request until `/v1/models` exposes both the classifier model and every catalog candidate.

## Debug Route Logs

Enable non-sensitive JSONL route decision logs:

```yaml
debug:
  enabled: true
  log_path: smart-model-router-decisions.jsonl
```

Each line includes the selected provider/model, strategy, preference, source, reason, and structured classifier attempt trace when applicable (per-attempt outcome, HTTP status, latency, verdict source, selected model, plus totals). The trace never carries raw classifier responses or error text. It does not log prompts, request bodies, credentials, API keys, or responses.

## Documentation

- `docs/configuration.md`: complete configuration reference.
- `docs/architecture.md`: package layout and routing pipeline.
- `docs/business-rules.md`: product/business routing rules.
- `docs/adr/`: architecture decision records.

## Development

Run tests:

```bash
go test ./...
```

Build the Linux shared library:

```bash
make build-local
```

See `INSTALL.md` for installation details.
