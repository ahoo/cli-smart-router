# ADR 0009: Distilled Intent Model for Task Detection

## Status

Accepted

## Context

Task detection (`DetectIntent`) drives `decision_engine` routing: the detected
task selects the configured route. The legacy detector is conservative keyword
matching (English + Portuguese substrings, e.g. "review", "error", "code").

A 37-case golden set labeled from historical error-log traffic (8 decidable +
29 ambiguous scaffolding) measured the legacy detector at 21/37 (56.8%):
decidable only 1/8 (planning/review/testing all missed), with 9 keyword false
positives on scaffolding ("Descreva…", "Reviewing routeFor…", heartbeat
checklists containing "error").

SystemOne (jev-1.13, choice over 8 intents + other) scored 27/37 (73.0%) on
the same set but costs ~1s and egress per request — unsuitable for the
per-request routing path.

## Decision

Ship a distilled student inside the domain layer (`internal/domain`,
embedded `intent_model.json`, ~5.9MB): multinomial logistic regression over
word (1,2) + char_wb (3,6) TF-IDF, trained on 214 SystemOne-labeled rows
(historical traffic + trilingual synthetic positives + git-ops hard
negatives), evaluated at 28/37 (75.7%) on the golden set.

`routingTask` now calls `DetectIntentSmart`: student first, legacy keyword
detection as fallback when the student is unavailable or below the
abstention threshold (artifact default 0.0, CV-selected; override via
`SetIntentModelThreshold`). Explicit `[router-task:]` / header overrides
still win over both.

## Consequences

- Routing behavior changes for prompts the legacy detector missed or
  misfired on; virtual-model decisions remain logged per request.
- No host callbacks, no egress, no latency change (inference is
  microseconds, pure Go, no CGo).
- Parity test (`testdata/intent_parity.json`) pins Go inference to the
  Python training output at 1e-9; golden regression test pins representative
  classifications.
- Raising the threshold trades decidable recall for ambiguous precision
  (eval-observed, not cross-validated); keep 0.0 unless re-validated on
  fresh traffic.
