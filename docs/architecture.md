# Smart Model Router Architecture

`smart-model-router` is a native CLIProxyAPI plugin that exposes one configurable virtual model and routes requests for that model to a configured built-in provider/model.

The implementation intentionally uses a minimal DDD-style layout. The goal is to keep routing policy testable and free from CLIProxyAPI ABI details, while keeping the native plugin entrypoint small and explicit.

## High-Level Flow

```text
Client request
  -> CLIProxyAPI
    -> model.route plugin call
      -> smart-model-router
        -> session route, if pinned
        -> cache route, if hit
        -> local capability-aware scoring (no host call)
        -> hybrid: use local decision if confident, else classifier
        -> llm: classifier first, always
        -> decision_engine: Prompt -> Context -> Complexity -> Policy Engine -> Router
        -> deterministic fallback (also the final answer for `capability`/`benchmark`)
      <- ModelRouteResponse(TargetKind: provider, Target, TargetModel)
    -> CLIProxyAPI executes selected provider/model
  <- Client response
```

By default, the plugin returns `TargetKind: provider`. CLIProxyAPI keeps responsibility for authentication, provider execution, retries, streaming, logging, and usage accounting.

The plugin only routes requests where `RequestedModel` matches the configured `virtual_model`. Every other model returns `Handled: false`.

## Package Layout

```text
cmd/plugin/
  main.go

internal/domain/
  config.go
  model.go
  policy.go
  capability.go
  intent.go
  context.go
  complexity.go
  policy_engine.go
  fallback.go

internal/application/
  router.go
  policy_router.go
  registrar.go
  usage.go

internal/infrastructure/
  cliproxy.go
  rpc.go
  yaml_config.go
  state.go
  runtime_state.go
  prompt.go
```

The `decision_engine` strategy adds five domain layers (`intent.go`, `context.go`, `complexity.go`, `policy_engine.go`, `fallback.go`) and one application layer (`policy_router.go`). They are additive and opt-in: the four legacy strategies (`capability`, `benchmark`, `llm`, `hybrid`) are unchanged.

## `cmd/plugin`

`cmd/plugin/main.go` is the native plugin boundary.

Responsibilities:

- Export the C ABI expected by CLIProxyAPI.
- Store and call the host callback API.
- Dispatch plugin RPC methods.
- Load and normalize plugin configuration.
- Handle `model.route`, `model.register`, `usage.handle`, management routes, and optional executor fallback.
- Call host callbacks such as `host.model.execute` and `host.http.do`.
- Write non-sensitive debug route logs.

This package is allowed to know about:

- C ABI details
- CLIProxyAPI method names
- host callbacks
- JSON envelopes
- runtime state

It should not contain reusable domain policy unless that policy depends directly on host/runtime behavior.

## `internal/domain`

The domain package contains pure routing concepts and policy logic.

It must stay free of:

- C ABI details
- host callback details
- HTTP execution details
- CLIProxyAPI host implementation details

### `config.go`

Defines plugin configuration structs and normalization rules.

Important types:

- `Config`
- `DebugConfig`
- `CatalogConfig`
- `PricingConfig`
- `CacheConfig`
- `ExecutorFallbackConfig`
- `ClassifierConfig`
- `RoutingConfig`
- `CandidateConfig`

Important defaults:

- `DefaultVirtualModel = "router:auto"`
- `DefaultStrategy = "capability"`
- `DefaultPreference = "balanced"`

Supported preferences:

- `cost`
- `balanced`
- `quality`

### `model.go`

Defines normalized routing model concepts.

Important types:

- `Candidate`
- `RouteDecision`

`CandidateFromConfig` converts user configuration into normalized domain candidates.

### `policy.go`

Contains deterministic, capability-aware candidate scoring used by every strategy's fallback path (and by `hybrid`'s local-first decision).

Responsibilities:

- Filter invalid candidates.
- Respect available providers.
- Estimate minimum required cost tier from simple prompt signals.
- Gate eligible candidates by capabilities inferred from the prompt (see `capability.go`), when at least one configured candidate declares a matching capability.
- Apply `preference` to rank the gated pool.
- Apply conservative post-classifier preference tiebreaks.

Key types and functions:

- `RouteScore`: one scored candidate, with `MatchedCount`, `InferredCount`, `Reason`, and `LocalConfident()`.
- `ScoreCandidates`: scores and sorts every eligible candidate for a prompt.
- `SelectCandidateWithConfidence`: returns the top `RouteScore`.
- `SelectCandidateForPrompt` / `SelectCandidate`: thin wrappers returning only the chosen `Candidate`, kept for simpler call sites that do not need confidence.
- `ApplyPreferenceTiebreak`: unchanged conservative post-classifier tiebreak.

Cost tiers:

- `low`
- `medium`
- `high`
- `very_high`

The minimum cost tier estimator uses conservative keyword groups: simple summary/classification can stay `low`; scripts, SQL, documentation, and rewrites start at `medium`; planning, coding, debugging, tests, and reviews start at `high`; architecture, migration, monorepo, production, security, and critical work start at `very_high`.

Quality tiers:

- `low`
- `medium`
- `high`
- `highest`

Unknown cost or quality values rank as `medium`.

### `capability.go`

Contains `InferCapabilities(prompt string) []string`, a small keyword-based matcher that maps prompt text to capability tags such as `coding`, `architecture`, `planning`, `summarize`, `translate`, `classify`, `tests`, `tools`, `agents`, `documentation`, `writing`, `review`, `refactor`, `reasoning`, `analysis`, and `long_context`.

It intentionally returns an empty slice for ambiguous prompts. Callers must treat an empty result as "no local capability signal", not as "no capabilities apply".

### Decision Engine layers (`decision_engine` strategy only)

The `decision_engine` strategy composes five additional pure domain layers. Each is independently testable and imports no host/ABI types. The order is Prompt -> Context -> Complexity -> Policy Engine, feeding the application-layer Model Router and, on execution failure, the Fallback Engine.

#### `intent.go` (Prompt Analyzer)

Classifies the extracted prompt into one `Intent` (a readable string enum): `planning`, `coding`, `review`, `testing`, `debug`, `security`, `documentation`, `performance`, or `IntentUnknown` (`""`) for ambiguous prompts.

- `DetectIntent(prompt string) Intent`
- `AnalyzePrompt(prompt string) PromptAnalysis` — `Intent` plus `Matched` so callers can tell a confident classification from an ambiguous one.

#### `context.go` (Context Analyzer)

Derives `ContextSignals` from a `ContextInput` (extracted prompt, raw body, message count, referenced files/tools): `FileCount`, `Language`, `ContextSize`, `ToolCount`, `HistoryTurns`, `DiffSize`. It holds only derived counts, sizes, and a language label — never prompts, bodies, or credentials.

- `AnalyzeContext(in ContextInput) ContextSignals`

#### `complexity.go` (Complexity Analyzer)

Produces a `ComplexityAssessment` from eight weighted, saturating factors (`ComplexityInput`: prompt length, file count, history turns, context size, tool count, token estimate, diff size, directory count):

- `Score` in `[0,100]`
- `MinCostTier` (`0=low..3=very_high`), aligned with cost ranking so the Policy Engine gates candidates consistently
- `Level` coarse bucket (`0=trivial..3=complex`)

Functions: `AssessComplexity(in ComplexityInput)` and `ComplexityInputFromSignals(prompt, ctx, files, tokenCount)`.

#### `policy_engine.go` (Policy Engine)

Evaluates the declarative `routes:` rules against `RouteFacts` (produced by the analyzers above). It is **table-driven**: every matchable dimension is one entry in a `conditions` slice, so adding a dimension means appending one entry, not scattering `if` statements. The Policy Engine only decides; it never scores text or executes anything.

- `EvaluateRoutes(...) PolicyDecision` — the single most specific matching rule (most active conditions wins; ties break by rule order; empty `when` is a catch-all with specificity 0).
- `EvaluateRoutesRanked(...) []PolicyDecision` — the full ordered, deduplicated chain of matching targets, used to build the fallback chain.

Each winning target is validated against configured candidates and available providers, so a rule pointing at an unknown or unavailable model is skipped and a less specific rule (or deterministic fallback) wins.

#### `fallback.go` (Fallback Engine)

Reacts to an execution failure by moving to the next policy-allowed target.

- `ClassifyFailure(outcome AttemptOutcome) FailureReason` — classifies `timeout`, `http_error`, `context_exceeded`, `token_limit`, `unavailable`, or `FailureNone`.
- `FailureReason.Fallbackable()` — whether the reason warrants trying the next model.
- `SelectNext(chain FallbackChain, failed map[string]struct{}, outcome AttemptOutcome) FallbackSelection` — the next untried target in the ranked chain.
- `MarkFailed(failed, provider, model)` — records a failed target.

`FallbackChain` is `[]PolicyDecision` produced by `EvaluateRoutesRanked`.

### Shared domain helpers

To keep candidate and availability handling identical across the Router, Policy Engine, and Model Router:

- `Config.Candidates() []Candidate` — the single `models -> []Candidate` conversion (replaces four inline loops).
- `AvailableSet(providers []string) map[string]struct{}` and `ProviderAvailable(provider, available)` — the single provider-availability builder and check, shared by `policy_engine.go`, `application/policy_router.go`, and `cmd/plugin/main.go`.

## `internal/application`

The application package contains use cases that coordinate domain logic and infrastructure contract types.

### `router.go`

The `Router` use case handles deterministic, capability-aware route selection.

It:

- Normalizes config.
- Ignores requests that do not match `virtual_model`.
- Converts configured models into domain candidates via `Config.Candidates()`.
- Extracts the last user message via `infrastructure.ExtractUserPrompt` (the same prompt used by cache keys and the classifier).
- Calls `domain.SelectCandidateWithConfidence`.
- Returns a `RouteDecision`, including `Confident` for callers implementing local-first strategies.

Classifier routing is not implemented here because classifier execution requires host callbacks. That host-aware behavior lives in `cmd/plugin/main.go`.

### `policy_router.go`

The `PolicyRouter` is the Model Router layer for the `decision_engine` strategy. It is intentionally **decision-free**: it receives a `PolicyDecision` from the Policy Engine, locates the matching provider among configured candidates and available providers, and forwards it. It never scores, ranks, or picks a model on its own.

- `Forward(decision, availableProviders) RouteDecision` — returns `Handled: false` (`policy_no_match` / `policy_provider_unavailable`) when the decision did not match or the target cannot be located, so the caller falls back to deterministic routing.
- `ForwardRequest(decision, req)` — convenience wrapper using the request's advertised providers.

The Policy Engine already resolves a concrete provider/model; the Model Router re-locates it independently. This is a deliberate layer boundary (the engine decides, the router locates and re-checks availability/disambiguation), not redundant work — it keeps model selection and provider location testable in isolation.

### `registrar.go`

Registers the virtual model with CLIProxyAPI.

The registered model is configurable through `virtual_model`.

### `usage.go`

Aggregates usage signals without storing sensitive request/response content.

## `internal/infrastructure`

The infrastructure package contains local contracts and state holders.

### `cliproxy.go`

Defines the minimal CLIProxyAPI JSON/ABI contract structs used by this plugin.

The project currently uses local contract structs instead of importing the CLIProxyAPI SDK because this workspace uses Go 1.22 and the local CLIProxyAPI SDK requires Go 1.26.

The SDK import decision should be revisited only when the project toolchain is Go 1.26 or newer.

### `rpc.go`

Builds and parses plugin RPC envelopes.

### `yaml_config.go`

Parses plugin YAML configuration from CLIProxyAPI plugin config payloads.

### `state.go`

Stores the current normalized configuration in memory.

### `runtime_state.go`

Stores non-sensitive runtime state.

Runtime state may include:

- catalog model IDs
- pricing fetch metadata
- usage aggregates
- session route pins
- route cache entries
- ephemeral, in-memory fallback chains (policy-allowed targets per session, used by the executor path; never persisted)
- counters, including `router_local_confident` for hybrid local-first decisions, `router_decision_engine`, and `router_fallback_<reason>`

Runtime state must not include:

- prompts
- request bodies
- response bodies
- credentials
- API keys
- auth records

#### Bounded in-memory maps

All three in-memory maps — the route cache, session routes, and fallback chains — are bounded by `defaultMaxEntries` (1024) and share a single generic LRU evictor, `evictLRU[V any](map[string]V, func(V) time.Time)`. Each value carries a `LastUsed` timestamp that is refreshed on read so eviction is meaningful. This prevents unbounded growth on long-running proxies: previously only the route cache was bounded, so `sessions` and fallback chains could accumulate one entry per `execution_session_id` forever.

`Snapshot()` deep-copies state via JSON marshal/unmarshal, so callers never alias the live maps.

### `prompt.go`

Extracts the last user message from OpenAI/Claude `messages` or Gemini `contents.parts` request bodies via `ExtractUserPrompt`. This is the single prompt-extraction implementation shared by cache keys, classifier input, and deterministic/local-first candidate scoring, so all three reason about the same text for a given request.

## Routing Pipeline

For a matching `virtual_model`, route selection happens in this order.

### 1. Session Route

If `routing.keep_same_model_per_session` is enabled and request metadata contains `execution_session_id`, the plugin checks whether that session is already pinned to a route.

If found, the route is reused and logged with `source: session`.

### 2. Route Cache

If `cache.enabled` is true, the plugin checks the route cache.

The cache key is based on:

- configured virtual model
- extracted last user message

The raw prompt is not persisted. Only the hash is stored as a map key.

Cache behavior:

- LRU eviction when `cache.max_entries` is reached.
- Optional TTL expiration with `cache.ttl`.
- State saves are throttled to avoid rewriting the state file on every request.

If found, the route is reused and logged with `source: cache`.

### 3. Local Capability-Aware Scoring

Every strategy computes a local, deterministic decision through `application.Router.Route`, which never makes a host call. This is the same decision used as the final answer for `capability`/`benchmark` and as the deterministic fallback for `llm`/`hybrid` on classifier failure.

Local scoring:

- extracts capabilities implied by the prompt via `domain.InferCapabilities` (a small keyword matcher, see `capability.go`);
- filters eligible candidates by validity, available providers, and minimum cost tier for the prompt (same as before);
- gates that pool to candidates declaring at least one inferred capability, when at least one configured candidate does so; otherwise the gate is a no-op;
- ranks the gated pool by `preference` (unchanged tier logic).

The result includes whether the decision is "locally confident": true only when the prompt matched a known capability signal and the winning candidate satisfies it. An ambiguous prompt, or a configuration with no matching `capabilities`, is never locally confident even though a candidate is still deterministically chosen.

### 4. Strategy-Specific Use Of The Local Decision And Classifier

- `capability` / `benchmark`: use the local decision directly. No classifier call.
- `llm`: always calls the classifier first (see below), regardless of local confidence. Deterministic fallback (the same local decision) is used only if the classifier fails.
- `hybrid`: local-first. If the local decision is confident, it is used directly and tagged `local_confident` in cache/debug reasons, skipping the classifier entirely. If not confident, the classifier is called exactly as in `llm`, with deterministic fallback on classifier failure.
- `decision_engine`: runs the rules pipeline (see "Decision Engine Pipeline" below) instead of the classifier. If a `routes:` rule matches and its target can be located, that decision is used; otherwise it falls through to the local deterministic decision from step 3. No classifier call.

### 5. Classifier Route

Classifier routing is attempted when:

- `strategy` is `llm`, always; or `strategy` is `hybrid` and the local decision above was not confident
- `classifier.enabled` is true
- at least one classifier model is configured

Classifier models are tried in order up to `classifier.max_attempts`.

The classifier call uses `host.model.execute` and sends an isolated routing prompt. It receives:

- the configured candidate catalog
- provider/model IDs
- cost tiers
- quality tiers
- capabilities
- the extracted last user message
- the configured `preference` instruction

The classifier request body starts from the per-model `request_overrides` map (generic passthrough for provider-specific options), then reapplies invariants `model`, `messages`, `stream: false`, `temperature: 0`, and `max_tokens` (`classifier.max_tokens`, default `500` when unset). Per-model `headers` are forwarded verbatim to `host.model.execute`. The system prompt treats the catalog and user text as untrusted data and asks only for the compact verdict first:

```json
{"selected_model":"<exact-id>"}
```

No reason or confidence is requested (legacy extra fields are still accepted; only `selected_model` is read). `classifier.timeout` is parsed but cannot be enforced because `host.model.execute` is synchronous and non-cancellable.

Verdict parsing searches `content`, `reasoning_content`, `reasoning`, then the raw body when appropriate. Each source is scanned for every balanced JSON object (an unfinished object never blocks a later valid one), and the first object naming an exact configured model whose provider is available wins. Unknown or unavailable selections are skipped so a later valid verdict can still win; when none is found the failure is categorized as `invalid_verdict` or `unavailable_selection`. Transport and HTTP failures are recorded as `transport_error` / `http_error`.

Classifier failure cases:

- host call error (`transport_error`)
- non-2xx model execution response (`http_error`)
- no usable verdict in any scanned source (`invalid_verdict`)
- selected model not found in configured `models` (skipped; `invalid_verdict` if nothing else matches)
- selected model provider unavailable (`unavailable_selection`)

On failure, the next classifier is tried. If all attempts fail, the trace-level `error` keeps the last attempt's outcome; `attempts_exhausted` is reserved for a configuration with no usable non-empty attempt, and `no_models` means none are configured. The local deterministic decision computed in step 3 is then used. A successful route carries `Reason: "classifier:<classifier-model-id>"`.

### 6. Preference Tiebreak

After a classifier selects a model, the plugin may apply a conservative preference tiebreak.

Behavior:

- `balanced`: no post-classifier tiebreak.
- `quality`: among candidates in the same cost tier as the classifier pick, prefer the highest quality.
- `cost`: among candidates in the same quality tier as the classifier pick, prefer the cheapest.

This is intentionally conservative. It does not override the classifier's complexity judgment by jumping across cost/quality tiers.

When a tiebreak changes the model, the route reason includes:

```text
preference_tiebreak:old-model->new-model
```

## Strategy Semantics

Supported strategy values:

- `capability`
- `benchmark`
- `llm`
- `hybrid`
- `decision_engine`

Current implementation behavior:

- `capability`: local capability-aware deterministic decision only. No classifier call.
- `benchmark`: currently behaves like `capability`; benchmark fields are reserved metadata.
- `llm`: classifier first on every request, local deterministic decision as fallback on classifier failure.
- `hybrid`: local-first. Uses the local decision directly when it is confident (skipping the classifier); otherwise behaves like `llm` for that request.
- `decision_engine`: runs the full rules pipeline (Prompt -> Context -> Complexity -> Policy Engine -> Router) driven by the `routes:` block. When no rule matches or the decided target cannot be located, it falls through to the same local deterministic decision, preserving backward compatibility.

The plugin metadata exposes all five values, and the local deterministic decision remains the safety path for every strategy.

## Decision Engine Pipeline

The `decision_engine` strategy is a deterministic, rules-driven pipeline layered on top of the analyzers. It never calls a classifier; every stage is pure domain logic. Its worst-case decision time is well under the 5ms budget (typically single-digit microseconds).

```text
request body
  -> Task override       (header/tag, when valid)    -> Task intent
  -> Prompt Analyzer     (domain.DetectIntent)       -> Task intent fallback
  -> Context Analyzer    (domain.AnalyzeContext)     -> language, file count, diff size, ...
  -> Complexity Analyzer (domain.AssessComplexity)   -> score [0,100] + tier
  -> RouteFacts assembled from the three analyzers
  -> Policy Engine       (domain.EvaluateRoutesRanked) -> ranked chain of allowed targets
  -> Model Router        (application.PolicyRouter.Forward) -> located provider route
```

Stages:

1. **Task override** (`main.go`, `intent.go`): uses a valid `X-Router-Task`, then a known `X-Router-Agent`, then matching user-message tags before heuristic analysis. This is intended for trusted agent orchestration.
2. **Prompt Analyzer** (`intent.go`): classifies the extracted last user message into one `Intent` when no override applies.
3. **Context Analyzer** (`context.go`): derives `ContextSignals` (language, file count, context size, tool count, history turns, diff size) from the prompt and raw body.
4. **Complexity Analyzer** (`complexity.go`): scores complexity `[0,100]` and derives the minimum cost tier.
5. **Policy Engine** (`policy_engine.go`): matches the `RouteFacts` against the declarative `routes:` rules table and returns the ranked chain of allowed targets, most specific rule first, ties broken by rule order. Targets are validated against configured candidates and available providers.
6. **Model Router** (`policy_router.go`): takes the winning `PolicyDecision` and locates the provider route. It makes no routing decision of its own.

When the ranked chain is non-empty and the first target can be located, that route is used and the full chain is cached (keyed by `execution_session_id`) so the executor fallback path can walk the remaining allowed targets on provider failure via the Fallback Engine (`fallback.go`). When the chain is empty or the target cannot be located, the pipeline abstains and the local deterministic decision is used instead.

A `decisionTrace` (task, language, complexity score, matched policy, provider, model, reason, decision time) is always produced for observability, even when the pipeline abstains, and the last outcome is exposed via the management status endpoint as `last_decision`.

## Preference Semantics

Supported preference values:

- `cost`
- `balanced`
- `quality`

Default: `balanced`

Invalid values normalize to `balanced`.

Preference affects:

- classifier prompt bias
- deterministic fallback ranking
- conservative post-classifier tiebreaks

Preference does not force every request to one extreme. A simple prompt should not be promoted to the most expensive model just because `preference: quality` is set.

## Provider Model Routing

The normal route response uses provider routing:

```json
{
  "Handled": true,
  "TargetKind": "provider",
  "Target": "claude",
  "TargetModel": "claude-sonnet-5",
  "Reason": "classifier:..."
}
```

Provider routing lets CLIProxyAPI keep its normal execution path.

The example config uses built-in provider keys:

- `claude`
- `codex`

## Optional Executor Fallback

When `executor_fallback.enabled` is true, the plugin declares executor capability.

For non-streaming requests, route responses may target the plugin itself:

```json
{
  "Handled": true,
  "TargetKind": "self",
  "Reason": "... executor_fallback"
}
```

The executor fallback then tries configured candidates through `host.model.execute`.

Limits:

- non-streaming only
- streaming executor fallback is not implemented
- disabled by default

## External State Refresh

The plugin may refresh external state before routing.

### Catalog Refresh

When `catalog.base_url` is configured, the plugin fetches:

```text
GET <base_url>/v1/models
```

through `host.http.do`.

The result is used as non-sensitive catalog state only. Configured candidate metadata remains authoritative.

### Pricing Refresh

When `pricing.enabled` and `pricing.url` are configured, the plugin fetches pricing metadata through `host.http.do`.

Routing must continue if this fetch fails.

The runtime state stores metadata such as byte count and error text, not sensitive payloads.

## Debug Logging

When `debug.enabled` and `debug.log_path` are configured, the plugin writes one JSON object per routed request.

Example fields:

- `time`
- `source`
- `virtual_model`
- `source_format`
- `stream`
- `strategy`
- `preference`
- `target_provider`
- `target_model`
- `reason`
- `classifier`

Possible `source` values:

- `selected`
- `cache`
- `session`

When `strategy: hybrid` uses the local confident decision instead of calling the classifier, `source` is still `selected`, and `reason` is prefixed with `local_confident` so decision logs distinguish it from a classifier or plain deterministic-fallback selection.

The classifier trace is structured and never carries raw classifier responses or error text. It includes:

- whether the classifier was enabled / used
- `attempt_count` and `total_latency_ms`
- per-attempt `provider`, `model`, `latency_ms`, `http_status`, `outcome` (`selected`, `transport_error`, `http_error`, `invalid_verdict`, `unavailable_selection`), plus `verdict_source` (`content`, `reasoning_content`, `reasoning`, `body`) and `selected_model` on success
- a coarse `error` category only (for example `no_models`, an outcome name, or `attempts_exhausted`)

Debug logs intentionally omit prompts and request/response bodies.

## Management API

The plugin registers a management route:

```text
GET /plugins/smart-model-router/status
```

The status response includes:

- plugin identifier
- virtual model
- strategy
- `virtual_model_status`: per-virtual-model effective classifier summary (enabled flag, models, max attempts, max tokens; headers and overrides excluded)
- usage snapshot
- runtime state snapshot
- `last_decision`: the last Decision Engine outcome (task, language, score, matched policy, provider, model, reason, matched flag, decision time in microseconds), when `strategy: decision_engine` has run. It contains only non-sensitive routing metadata.

## Security Boundaries

The plugin must not log or persist:

- prompts
- request bodies
- response bodies
- credentials
- API keys
- auth records

Debug logs and state are intentionally non-sensitive.

Classifier routing receives the extracted last user message in memory because it must classify the request. That text is not written to the plugin state or debug logs.

## Design Constraints

- Keep domain code independent from ABI and host callback details.
- Keep configured model metadata authoritative.
- Keep deterministic fallback available for every advanced feature.
- Avoid interfaces with one implementation unless they protect domain/application code from host or ABI details.
- Do not import the CLIProxyAPI SDK until the project toolchain can use Go 1.26 or newer.
