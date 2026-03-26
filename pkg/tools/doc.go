// Package tools contains built-in, reusable tools that can be plugged into
// agentcore.
//
// Why this package exists:
// `pkg/agentcore/tooling` defines the stable tool contract used by the execution
// core, but it should not accumulate every concrete tool implementation over
// time.
//
// `pkg/tools` is the first layer above the core for built-in capabilities that
// have clear long-term value as reusable execution tools.
//
// The goal is to keep this package small and intentional. A tool should be
// added here only if it has clear long-term value as a reusable capability.
package tools
