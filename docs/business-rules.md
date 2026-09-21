# Smart Model Router Business Rules

This document describes the business rules implemented by `smart-model-router`.

The rules below describe product behavior, not low-level implementation details.

## Core Rule: Route Only the Configured Virtual Model

The plugin only handles requests whose requested model exactly matches `virtual_model`.

If the request model does not match `virtual_model`, the plugin must return `Handled: false`.

Default virtual model:

```yaml
virtual_model: router:auto
```

A deployment may use a different alias, for example:

```yaml
virtual_model: claude-auto
```

## Provider Execution Rule

The default routing path must return a provider route:

```text
TargetKind: provider
Target: <provider>
TargetModel: <model>
```

This keeps provider authentication, request execution, retries, logging, streaming, and usage accounting inside CLIProxyAPI.

The plugin should not execute the final user request itself unless `executor_fallback.enabled` is explicitly enabled.

## Authoritative Model Metadata Rule

The configured `models` list is authoritative for:

- provider mapping
- model ID
- capabilities
- cost tier
- quality tier

`capabilities` is not documentation-only: it directly gates local capability-aware routing (see Local Capability-Aware Routing Rule) in addition to being sent to the classifier.

`/v1/models` may be used to validate or enrich availability state, but it must not be used to infer provider or capability metadata for unmapped models.

## Candidate Validity Rule

A candidate model is routable only when it has at least:

- non-empty `provider`
- non-empty `model`

If CLIProxyAPI provides an available provider list, the candidate provider must be present in that list.

## Strategy Rule

Supported strategy values are:

- `capability`
- `benchmark`
- `llm`
- `hybrid`
- `decision_engine`

Current behavior:

- `capability`: local capability-aware deterministic decision only. No classifier call.
- `benchmark`: currently behaves like `capability`; benchmark fields are reserved for future scoring.
- `llm`: classifier first on every request, local deterministic decision as fallback on classifier failure.
- `hybrid`: local-first. Uses the local decision directly when it is locally confident, skipping the classifier; otherwise behaves like `llm` for that request.
- `decision_engine`: runs the declarative rules pipeline (Prompt -> Context -> Complexity -> Policy Engine -> Model Router) driven by the `routes:` block. When no rule matches, or the decided target cannot be located, it falls back to the local deterministic decision.

Every strategy must keep the local deterministic decision available so the router remains operational when advanced features fail.

## Local Capability-Aware Routing Rule

Before any classifier call, the router always computes a local decision from configuration alone:

1. Infer capability tags from the extracted last user message using a small keyword matcher (for example: "resuma" implies `summarize`; "arquitetura" implies `architecture`/`planning`; "erro"/"bug" implies `coding`/`review`).
2. Filter candidates the same way as deterministic fallback always has: valid provider/model, available provider, and minimum cost tier for the prompt.
3. If the prompt matched at least one inferred capability and at least one eligible candidate declares it, keep only candidates declaring a matching capability. Otherwise, this step is a no-op and every eligible candidate remains in the pool.
4. Rank the remaining pool by `preference`, exactly as the pre-existing deterministic ranking rules describe below.

The local decision is "confident" only when both of these hold:

- the prompt matched at least one known capability signal;
- the winning candidate declares at least one of the matched capabilities.

An ambiguous prompt (no capability signal matched) or a configuration where no candidate declares a matching capability is never confident, even though a candidate is still deterministically chosen by cost/quality tier ranking.

`hybrid` uses this confidence to decide whether to call the classifier. `capability`, `benchmark`, and the fallback path of `llm` use the local decision regardless of confidence, because they have no classifier step (or the classifier already failed).

## Decision Engine Rule

When `strategy` is `decision_engine`, the router runs a deterministic, rules-based pipeline before any deterministic fallback:

1. Prompt Analyzer: classify the extracted last user message into a task Intent (`planning`, `coding`, `review`, `testing`, `debug`, `security`, `documentation`, `performance`, or unknown).
2. Context Analyzer: derive non-sensitive signals (file count, detected language, context size, tool count, history turns, diff size).
3. Complexity Analyzer: produce a `[0,100]` complexity score, a coarse level, and a minimum cost tier from eight weighted factors.
4. Policy Engine: match the request facts against the configured `routes:` rules and produce the ordered chain of allowed targets.
5. Model Router: forward the winning target; the Router only locates the provider/model, it does not score or pick.

The Policy Engine decides and the Model Router forwards. These responsibilities must stay separate: the Router must never re-score or re-rank.

When no rule matches, or the decided target cannot be located among configured, available candidates, the pipeline abstains and the local deterministic decision is used. This keeps full backward compatibility: `routes:` is optional and its absence changes nothing.

A non-sensitive decision trace (task, language, complexity score, matched policy, provider, model, reason, decision time) is recorded for the last decision regardless of whether a rule matched.

## Policy Engine Rule

The Policy Engine evaluates declarative `routes:` rules against request facts. It does no text analysis itself; it only matches facts the analyzers already produced.

Rule matching:

- Every `when` condition is optional; an omitted condition is a wildcard.
- Supported conditions: `task`, `language`, `complexity` (coarse tier), `complexity_min`/`complexity_max` (numeric score bounds), `min_files`, `has_diff`, `stream`.
- The most specific matching rule wins, where specificity is the number of active conditions the rule matched.
- Ties break by rule order: the first rule wins.
- A rule with an empty `when` is a catch-all with specificity 0.

Target validation:

- A rule is skipped when its target `model` is empty, is not present in configured `models`, or its provider is unavailable.
- When a rule sets `provider`, both provider and model must match a configured candidate; otherwise the first candidate with the model id is used.
- Skipping an invalid rule lets a less specific rule (or deterministic fallback) win, so a misconfigured rule never breaks routing.

## Fallback Engine Rule

The Policy Engine produces an ordered chain of allowed targets, not just one. The Fallback Engine walks that chain to pick the next allowed model after a provider failure.

A failure is fallbackable when it is a timeout, HTTP error, context exceeded, token limit, or provider unavailability. Any other outcome (including success) is not fallbackable and stops the walk.

Failed provider/model pairs are marked and skipped so the engine never retries the same failed target. When the chain is exhausted, the request fails with the last error.

The fallback chain is ephemeral, per-session, in-memory, and never persisted. It is bounded like the other in-memory maps so a long-running proxy does not accumulate one chain per session forever.

## Preference Rule

Supported preference values are:
- `cost`
- `balanced`
- `quality`

Default: `balanced`

Invalid or empty values must normalize to `balanced`.

Preference influences decisions but does not blindly override task complexity.

Business meaning:

- `cost`: choose the cheapest acceptable model.
- `balanced`: balance cost and quality.
- `quality`: choose the highest-quality model that is appropriate for the task.

## Classifier Rule

When `strategy` is `llm`, the plugin always attempts semantic classification before using the local deterministic decision, provided `classifier.enabled` is true.

When `strategy` is `hybrid` and `classifier.enabled` is true, the plugin attempts semantic classification only when the local capability-aware decision (see Local Capability-Aware Routing Rule) is not confident. A confident local decision is used directly and the classifier is not called for that request.

The classifier must choose a model from the configured `models` catalog.

Classifier models are tried in configured order up to `classifier.max_attempts`.

The classifier must receive an isolated routing prompt, not the original full request body as a chat continuation.

The classifier receives:

- configured provider/model catalog
- model capabilities
- cost tiers
- quality tiers
- current `preference`
- extracted last user message

The classifier request carries `max_tokens` from `classifier.max_tokens` (default `500` when unset; the current intended deployment uses `1000`), per-model `headers` forwarded verbatim, and per-model `request_overrides` (a generic YAML map for provider-specific options) merged underneath routing invariants. `model`, `messages`, `stream`, `temperature`, and `max_tokens` always win over conflicting overrides. `classifier.timeout` is parsed but cannot be enforced because `host.model.execute` is synchronous and non-cancellable.

The routing prompt treats the catalog and user text as untrusted data and asks only for the compact verdict:

```json
{"selected_model":"<exact-id>"}
```

No reason or confidence is requested. Legacy responses carrying extra fields are still accepted; only `selected_model` is read.

## Classifier Failure Rule

Verdict parsing must search `content`, `reasoning_content`, `reasoning`, then the raw body when appropriate, scanning every balanced JSON object in each source. The first object naming an exact configured model whose provider is available wins. Unknown selections are skipped so a later valid verdict can still win; unavailable selections are recorded separately.

Classifier output must be rejected when:

- the host callback fails (`transport_error`)
- the classifier response is non-2xx (`http_error`)
- no scanned source contains a usable verdict (`invalid_verdict`)
- every named model is unknown, or every known model has an unavailable provider (`invalid_verdict` / `unavailable_selection`)

If one classifier attempt fails, the next configured classifier may be tried.

If all classifier attempts fail, deterministic fallback must be used.

The classifier trace must carry only structured attempt outcomes (outcome, HTTP status, latency, verdict source, selected model, totals); it must never log raw classifier responses or error text. A successful classifier route must carry `Reason: "classifier:<classifier-model-id>"`, not classifier-supplied prose.

## Last User Message Rule

Routing classification, cache keys, and local capability-aware scoring must all use the same extracted last user message, not the full conversation history or raw request body.

Reason:

- The same user prompt should route deterministically even when prior conversation history differs.
- Historical context from tools or previous messages should not dominate model selection for the current request.
- Using the same extracted text for cache keys, the classifier, and local scoring keeps all three decision paths consistent for the same request; scoring the raw request body could otherwise disagree with the cache key or classifier input.

The plugin must support common request body shapes such as OpenAI/Claude `messages` and Gemini `contents.parts`.

## Subagent Task Override Rule

Trusted clients may set `X-Router-Task` to a valid task intent. The router uses
it before keyword analysis. When that header is absent or invalid, it accepts a
known `X-Router-Agent` (`planner`, `implementer`, or `reviewer`), then the
equivalent `[router-task: ...]` and `[router-agent: ...]` tags from the last
user message. Invalid declarations fall back to normal lexical analysis.

The effective override is included in cache-key material. This prevents a route
selected for a planning prompt from being reused for an implementer invocation
with the same prompt text.

## Route Cache Rule

When `cache.enabled` is true, repeated identical prompts should reuse the same route decision.

Cache key material:

- configured virtual model
- extracted last user message

The raw prompt must not be persisted. Only a hash is used as the route cache key.

Cache entries must be bounded by `cache.max_entries`.

When full, the least recently used entry must be evicted.

If `cache.ttl` is set, entries older than the TTL must be re-evaluated.

## Session Affinity Rule

When `routing.keep_same_model_per_session` is true, a known session route should be reused before cache or classifier selection.

Session ID source:

```text
metadata.execution_session_id
```

Session routes are non-sensitive because they store only provider/model/reason metadata.

## Decision Priority Rule

For matching virtual-model requests, route selection order is:

1. Existing session route.
2. Existing cache route.
3. Local capability-aware deterministic decision, computed always (no classifier call).
4. For `decision_engine`: run the rules pipeline (Prompt -> Context -> Complexity -> Policy Engine -> Model Router). Use its target when a rule matches and the target is locatable; otherwise fall through to the local decision from step 3.
5. For `hybrid`: use the local decision directly if confident; otherwise call the classifier.
6. For `llm`: call the classifier regardless of local confidence.
7. If the classifier is called and fails, use the local decision from step 3 (this is the deterministic fallback).

Newly selected routes should be cached when cache is enabled and pinned to the session when session affinity is enabled.

## Deterministic Fallback Rule

The local decision selects from configured candidates without calling any LLM. It is used directly by `capability`/`benchmark`, and as the fallback for `llm`/`hybrid` classifier failure, and as the direct decision for `hybrid` when confident.

It must:

- filter invalid candidates
- respect available providers
- estimate the minimum required cost tier from simple prompt signals
- gate eligible candidates by capabilities inferred from the prompt, when at least one eligible candidate declares a matching capability (see Local Capability-Aware Routing Rule)
- apply `preference` to rank the gated pool

Current prompt signals for minimum cost tier:

- Simple arithmetic/classification/summary/translation requests can use `low` cost models.
- Script, SQL, regex, documentation, formatting, and rewrite requests require at least `medium` cost models.
- Planning, strategy, explanation, analysis, coding, implementation, refactor, debug, test, and review requests require at least `high` cost models.
- Architecture, system design, migration, monorepo, production, security, or critical requests require at least `very_high` cost models.

Current prompt signals for capability inference include: `resuma`/`summarize` implies `summarize`; `traduza`/`translate` implies `translate`; `classifique`/`classify` implies `classify`; `erro`/`bug`/`falha`/`exception` implies `coding`/`review`; `refatora`/`refactor` implies `refactor`/`coding`; `arquitetura`/`architecture`/`design`/`planeje` implies `architecture`/`planning`; `explique`/`explain`/`analise`/`compare` implies `reasoning`/`analysis`; `teste`/`unit test`/`coverage` implies `tests`/`coding`; `agente`/`agent`/`ferramenta`/`tool` implies `agents`/`tools`; `documenta`/`documentation` implies `documentation`/`writing`; `escreva`/`write` implies `writing`; `revis`/`review` implies `review`; prompts longer than roughly 1200 characters imply `long_context`.

Ranking by preference within the gated pool:

- `quality`: highest quality first, cost as secondary tie-break.
- `cost`: lowest cost first, quality as secondary tie-break.
- `balanced`: lowest cost first, quality as secondary tie-break.

## Preference Tiebreak Rule

After classifier selection, preference may apply a conservative tiebreak among equivalent-tier candidates.

Rules:

- `balanced`: do not alter the classifier pick.
- `quality`: within the same cost tier as the classifier pick, prefer the highest quality.
- `cost`: within the same quality tier as the classifier pick, prefer the cheapest.

The tiebreak must not cross tiers because it should not override the classifier's complexity judgment.

When a tiebreak changes the model, the route reason must show the change:

```text
preference_tiebreak:old-model->new-model
```

## Cost Tier Rule

Recognized cost tiers:

- `low`
- `medium`
- `high`
- `very_high`

Unknown or empty cost values rank as `medium`.

## Quality Tier Rule

Recognized quality tiers:

- `low`
- `medium`
- `high`
- `highest`

Unknown or empty quality values rank as `medium`.

## Debug Logging Rule

When debug logging is enabled, the plugin writes one JSON line per routed request.

Logs may include:

- timestamp
- decision source
- virtual model
- source format
- stream flag
- strategy
- preference
- target provider/model
- reason
- classifier trace (structured attempt outcomes only; never raw classifier responses or error text)
- decision trace (for `decision_engine`: task, language, complexity score, matched policy, decision time)

Logs must not include:

- prompts
- full request bodies
- credentials
- API keys
- auth records
- response bodies

## Runtime State Rule

Runtime state may persist only non-sensitive data:

- catalog IDs
- pricing metadata
- aggregate usage
- route cache
- session routes
- counters
- last decision-engine outcome (task, language, score, policy, provider, model, reason, decision time)

The in-memory maps (route cache, session routes, and the ephemeral decision-engine fallback chains) are all bounded by a shared LRU limit (default 1024 entries) so a long-running proxy does not grow memory without limit. Fallback chains are never persisted to disk; they are short-lived and rebuilt per request.

Runtime state must not persist:

- prompts
- request bodies
- response bodies
- credentials
- API keys
- auth records

## Pricing Rule

Pricing metadata fetches are optional.

If pricing refresh fails, routing must continue.

Current routing does not enforce `routing.max_cost_per_request` as a hard limit.

## Catalog Rule

Catalog refresh from `/v1/models` is optional.

If catalog refresh fails, routing must continue.

Configured `models` remain authoritative even when catalog data is available.

## Executor Fallback Rule

Executor fallback is opt-in.

When disabled, the plugin returns provider routes.

When enabled, non-streaming requests may be routed to the plugin itself for same-request fallback attempts.

Streaming executor fallback is not implemented.

## Management Rule

The plugin exposes a management status route:

```text
GET /plugins/smart-model-router/status
```

It reports non-sensitive status, usage snapshots, and runtime state snapshots, including a per-virtual-model classifier summary (enabled flag, models, max attempts, max tokens; headers and overrides excluded).

## Safety Rule

The router must remain useful even when optional features fail.

Failures in classifier, catalog, pricing, cache lookup, or state persistence must not prevent deterministic routing unless no valid configured candidate exists.
