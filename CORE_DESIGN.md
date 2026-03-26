# Core Design

## Positioning

This repository is not trying to become a full agent platform.
It is trying to provide a stable, focused, and extensible agent execution core.

The core should be:

- stable in its contracts
- small enough to understand
- explicit in its semantics
- extensible by composition

Advanced capabilities should be built on top of the core instead of being mixed into it too early.

## Core Responsibility

`agentcore` is responsible for the execution semantics of one agent turn.

That includes:

- provider abstraction
- tool abstraction
- context assembly
- in-memory session abstraction
- turn loop execution
- streaming support
- runtime events

In short:

`agentcore` answers the question:
"How does one agent turn execute correctly and predictably?"

## Non-Goals For The Core

The core should not directly own higher-level product features.

That includes:

- channel adapters
- UI rendering
- scheduling
- workflow products
- business-specific multi-agent policies
- plugin marketplaces
- large configuration systems
- persistence strategy beyond the minimum interface needed by execution

These may exist in the repository later, but they should be layered above the core.

## Layering

The repository is organized around explicit layering:

- `pkg/agentcore/...`
  The stable execution core intended for external reuse.

- `cmd/...`
  Runnable entrypoints and demos.

- `internal/...`
  Repository-local support code that is useful for examples or experiments,
  but is not part of the external API surface.

- `ref/...`
  Reference implementations and source material used for study and extraction.

Future higher-level modules should live outside `pkg/agentcore`.
They can be placed in sibling packages under `pkg/` when they become reusable,
or under `internal/` while still experimental.

## Stable Contracts

The following concepts should remain stable and easy to reason about:

- `provider.ChatModel`
- `provider.StreamingChatModel`
- `tools.Tool`
- `tools.Registry`
- `session.Store`
- `agent.ContextBuilder`
- `agent.Loop`
- `agent.EventBus`
- `agent.EventMeta`

These are the backbone of the execution layer.

## Extension Strategy

The preferred extension mechanisms are:

- composition
- replacement behind interfaces
- subscription to runtime events
- higher-level orchestration built above the loop

The preferred non-strategy is:

- putting every new feature directly into `agentcore`

## Current Scope Decision

For now, session state remains in memory on purpose.
This keeps the implementation small and lets us focus on execution semantics first.

If summary, compression, retries, or subagents are added later, they should be introduced in a way that preserves the small and stable execution core.
