# Installation

This document explains how to build, install, and configure the `smart-model-router` CLIProxyAPI plugin.

## Requirements

- Go 1.22 or newer for this repository.
- CGO enabled.
- A CLIProxyAPI build with plugin support.
- `plugins.enabled: true` in CLIProxyAPI configuration.

Check CGO:

```bash
go env CGO_ENABLED
```

If it returns `0`, enable CGO for the build:

```bash
CGO_ENABLED=1 go build -buildmode=c-shared -o smart-model-router.so ./cmd/plugin
```

## Build

Recommended local build:

```bash
make build-local
```

Build all configured targets:

```bash
make build-all
```

Cross-compiling CGO shared libraries requires target C compilers. Override Makefile compiler variables when your toolchain names differ, for example:

```bash
make build-linux LINUX_ARM64_CC=aarch64-linux-gnu-gcc
```

Manual commands:

Linux:

```bash
go test ./...
CGO_ENABLED=1 go build -buildmode=c-shared -o smart-model-router.so ./cmd/plugin
```

macOS:

```bash
go test ./...
CGO_ENABLED=1 go build -buildmode=c-shared -o smart-model-router.dylib ./cmd/plugin
```

Windows uses `.dll`, but this has not been verified in this workspace:

```bash
go test ./...
CGO_ENABLED=1 go build -buildmode=c-shared -o smart-model-router.dll ./cmd/plugin
```

## Install

Copy the built dynamic library into the CLIProxyAPI plugin directory.

Recommended platform-specific path:

```text
plugins/<GOOS>/<GOARCH>/smart-model-router.so
```

Simple fallback path:

```text
plugins/smart-model-router.so
```

The plugin ID is derived from the file name without extension. The configuration key must therefore be:

```yaml
plugins:
  configs:
    smart-model-router:
```

## Configure CLIProxyAPI

Add or update the CLIProxyAPI config:

You can start from the complete example in this repository:

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
        timeout: 8s # parsed but not enforceable (sync host call); see docs/configuration.md
        max_attempts: 2
        max_tokens: 1000
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

Only requests using the configured `virtual_model` are intercepted. For example, if you set:

```yaml
virtual_model: smart:auto
```

then clients must send:

```json
{"model":"smart:auto"}
```

## Verify

Start or restart CLIProxyAPI after installing the plugin.

Verify plugin status:

```http
GET /v0/management/plugins
```

Expected plugin fields:

```json
{
  "registered": true,
  "effective_enabled": true
}
```

Verify model registration:

```http
GET /v1/models
```

The response should include the configured virtual model, for example `router:auto`.

Verify management status:

```http
GET /v0/management/plugins/smart-model-router/status
```

This endpoint requires the CLIProxyAPI management key.

## Send A Routed Request

OpenAI-compatible example:

```json
{
  "model": "router:auto",
  "messages": [
    {"role": "user", "content": "Write a small Go function."}
  ]
}
```

The plugin routes the request to a configured candidate whose provider is available in CLIProxyAPI. With `strategy: llm` or `strategy: hybrid`, the classifier can select among configured candidates using an isolated routing prompt that asks only for `{"selected_model":"<exact-id>"}`; unknown, unavailable, or unparsable verdicts fall back to deterministic routing. Provider-specific classifier options (for example Qwen/SiliconFlow parameters) go in per-model `request_overrides`; see `docs/configuration.md`.

`preference` controls the cost/quality bias:

```yaml
preference: balanced # cost, balanced, or quality
```

Route cache is deterministic for repeated prompts:

```yaml
cache:
  enabled: true
  max_entries: 1024
  ttl: 24h
```

Repeated identical prompts should use `source: cache` in the debug JSONL log after the first selection.

For complete configuration details, see `docs/configuration.md`.

## Same-Request Fallback

Enable non-streaming same-request fallback explicitly:

```yaml
executor_fallback:
  enabled: true
  max_attempts: 3
```

When enabled, non-streaming requests for `virtual_model` route to the plugin's own executor. The executor tries configured candidates in order through `host.model.execute`. Streaming fallback is not implemented yet; streaming requests continue through provider routing.

## Upgrade

Dynamic libraries may remain loaded by the running process. After rebuilding the plugin, unload it through the CLIProxyAPI Management API or restart CLIProxyAPI before replacing the artifact.

Do not rely on overwriting a loaded `.so`, `.dylib`, or `.dll` in place.
