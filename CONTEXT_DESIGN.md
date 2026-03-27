# Context Design

## Session vs Active Context

`session` is long-term conversation state.
`active context` is the transient message array sent to the model for one call.

They are intentionally different things.
Budget pressure may change active context assembly, but it must not silently
change session semantics.

## Budget and Telemetry

Budget is an execution constraint on model input, not a storage limit on the
session transcript.

The runtime therefore enforces budget immediately before each model call and
emits telemetry for:

- estimated active-context size
- configured budget ceiling
- configured target after compaction
- the iteration that triggered compaction
- before/after compaction deltas

The first implementation uses a provider-agnostic estimator and a simple active
context compactor. Exact tokenizer integration can be added later without
changing the loop boundary.

## Compaction Principles

First-pass compaction only mutates active context.
It does not rewrite session history and does not persist summaries.

`agentcore` should provide stable hooks for:

- budget checks
- compaction
- telemetry events

Specific strategies remain replaceable. The default strategy keeps the system
prompt and the most recent messages, but future implementations may use summary
or archival flows above the same core boundary.
