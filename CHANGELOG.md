# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Support `X-Router-Task`, `X-Router-Agent`, `[router-task: ...]`, and
  `[router-agent: ...]` routing overrides for OpenCode subagents.
- Expose declarative `routes` in the CLIProxyAPI plugin configuration schema.
- Support multiple independently routable virtual models via `virtual_models`.
  Each entry has its own strategy, preference, models, routes, classifier,
  cache, and routing. Legacy single `virtual_model` configs keep working.
  See `docs/adr/0007-multi-virtual-models.md` and
  `configs/smart-model-router_multi.yaml`.
