package profile

import (
	"fmt"
	"strings"
)

// PromptConfig controls optional prompt-layer guidance blocks.
type PromptConfig struct {
	EnableExecutionGuidance *bool `toml:"enable_execution_guidance"`
	EnableScenarioGuidance  *bool `toml:"enable_scenario_guidance"`
}

// CapabilityGuidanceOptions declares which reusable capabilities are active.
type CapabilityGuidanceOptions struct {
	Skills     bool
	Delegation bool
	Automation bool
	CodeExec   bool
}

// DelegationPromptInput declares task-scoped child prompt details.
type DelegationPromptInput struct {
	Goal           string
	ContextSummary string
	WorkDir        string
}

// AutomationPromptInput declares unattended job prompt details.
type AutomationPromptInput struct {
	Task string
}

// CodeExecPromptInput declares code execution prompt details.
type CodeExecPromptInput struct {
	AllowedTools []string
}

// BuildDefaultIdentity returns the default identity block for an agent profile.
//
// Why:
// Identity answers "what kind of agent is this?" and should remain stable
// across many tasks. It is different from the deeper behavioral rules in Soul.
func BuildDefaultIdentity() string {
	return strings.TrimSpace(`
You are a general-purpose local agent.
You operate in the current environment and use available tools to help complete user requests.
`)
}

// BuildDefaultSoul returns the default behavioral rules for an agent profile.
//
// Why:
// Soul captures the durable working method of the agent: how it should inspect,
// decide, act, and report. Keeping this separate from Identity makes the prompt
// easier to reason about and easier to override intentionally.
func BuildDefaultSoul() string {
	return strings.TrimSpace(`
Understand the current state before acting.
Use tools to inspect facts instead of guessing.
Take the smallest action that makes real progress.
Do not repeat tool calls without new information.
Report failures clearly and stay concise.
`)
}

// BuildExecutionGuidancePrompt returns the execution-discipline layer.
func BuildExecutionGuidancePrompt() string {
	return strings.TrimSpace(`
# Execution Guidance
When you say you will inspect, run, verify, or change something, use the available tools immediately instead of ending the turn with a plan.
If required information can be retrieved from files, commands, or available tools, retrieve it before answering; do not guess when inspection is possible.
Before finalizing, verify that the task is actually complete and that the result matches the user's request.
When a recoverable step fails or returns partial information, try another grounded strategy before giving up.
`)
}

// BuildCapabilityGuidancePrompt returns capability-aware guidance for enabled modules.
func BuildCapabilityGuidancePrompt(opts CapabilityGuidanceOptions) string {
	parts := make([]string, 0, 4)
	if opts.Skills {
		parts = append(parts, "When skills are available, inspect and reuse them instead of reinventing known workflows.")
	}
	if opts.Delegation {
		parts = append(parts, "Use delegation only when a task can be cleanly isolated; child work should stay scoped and summarized.")
	}
	if opts.Automation {
		parts = append(parts, "Automation runs execute without a live user present, so unattended tasks must be self-contained and directly deliverable.")
	}
	if opts.CodeExec {
		parts = append(parts, "Use code execution to compress repetitive tool chains, not to bypass normal reasoning or runtime policy.")
	}
	if len(parts) == 0 {
		return ""
	}
	return "# Capability Guidance\n" + strings.Join(parts, "\n")
}

// BuildEnvironmentPrompt renders runtime environment facts for the main loop.
func BuildEnvironmentPrompt(env EnvironmentInfo) string {
	scope := strings.TrimSpace(env.FilesScope)
	if scope == "" {
		scope = "workspace"
	}

	enabledTools := "none"
	if len(env.EnabledTools) > 0 {
		enabledTools = strings.Join(env.EnabledTools, ", ")
	}

	fileRoots := "current working directory"
	if len(env.FileRoots) > 0 {
		fileRoots = strings.Join(env.FileRoots, ", ")
	}

	maxIterations := env.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 8
	}

	return strings.TrimSpace(fmt.Sprintf(`
# Environment
Current working directory: %s
File access scope: %s
Allowed file roots: %s
Enabled tools: %s
Max tool-call iterations per turn: %d
`, env.WorkDir, scope, fileRoots, enabledTools, maxIterations))
}

// BuildSystemPrompt composes the final system prompt from layered builders.
func BuildSystemPrompt(identity, soul string, env EnvironmentInfo, skillsSummary string, capabilities CapabilityGuidanceOptions, promptCfg PromptConfig) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		identity = BuildDefaultIdentity()
	}

	soul = strings.TrimSpace(soul)
	if soul == "" {
		soul = BuildDefaultSoul()
	}

	parts := []string{
		"# Identity\n" + identity,
		"# Soul\n" + soul,
	}
	if promptExecutionEnabled(promptCfg) {
		parts = append(parts, BuildExecutionGuidancePrompt())
	}
	if promptScenarioEnabled(promptCfg) {
		if capabilityPrompt := BuildCapabilityGuidancePrompt(capabilities); strings.TrimSpace(capabilityPrompt) != "" {
			parts = append(parts, capabilityPrompt)
		}
	}
	if strings.TrimSpace(skillsSummary) != "" {
		parts = append(parts, skillsSummary)
	}
	parts = append(parts, BuildEnvironmentPrompt(env))
	return strings.Join(parts, "\n\n---\n\n")
}

// BuildDelegationPrompt returns the task-scoped child system prompt.
func BuildDelegationPrompt(input DelegationPromptInput) string {
	parts := []string{
		"You are a focused child worker handling one delegated task. You are not talking directly to the end user.",
		fmt.Sprintf("Task: %s", strings.TrimSpace(input.Goal)),
	}
	if strings.TrimSpace(input.ContextSummary) != "" {
		parts = append(parts, "Context:\n"+strings.TrimSpace(input.ContextSummary))
	}
	if strings.TrimSpace(input.WorkDir) != "" {
		parts = append(parts, "Working directory:\n"+strings.TrimSpace(input.WorkDir))
	}
	parts = append(parts,
		"Complete only the assigned task. Do not expand the scope on your own.",
		"Use the provided workdir when available; if it is missing, discover the correct path before acting and do not invent repository paths.",
		"Return a concise summary containing what you accomplished, relevant outputs or modified files, and any blocking issue.",
		"Do not return your full execution trace or hidden reasoning; the parent only needs the summary.",
	)
	return strings.Join(parts, "\n\n")
}

// BuildAutomationPrompt returns the unattended job wrapper prompt.
func BuildAutomationPrompt(input AutomationPromptInput) string {
	return strings.TrimSpace(fmt.Sprintf(`
You are running as an unattended automation job in a fresh session.
There is no current chat context and no live user available for clarification.
Treat the following task instruction as fully self-contained and produce output that is directly deliverable.
Do not respond with follow-up questions unless the task is impossible to continue at all.
Do not create or schedule additional automation jobs from inside this run.

Task instruction:
%s
`, strings.TrimSpace(input.Task)))
}

// BuildCodeExecPrompt returns the wrapper instructions for future code execution entrypoints.
func BuildCodeExecPrompt(input CodeExecPromptInput) string {
	allowedTools := "none"
	if len(input.AllowedTools) > 0 {
		allowedTools = strings.Join(input.AllowedTools, ", ")
	}
	return strings.TrimSpace(fmt.Sprintf(`
This code-execution unit exists to compress repetitive multi-step tool work into one bounded execution.
It does not replace normal reasoning and must stay within runtime policy.
Only the following tools may be used from inside code execution: %s.
Do not invoke delegation, automation, or nested code execution from inside this execution unit.
Keep script output concise and return only the final information needed by the caller; avoid echoing intermediate noise.
Prefer local processing inside the script and only send essential tool requests back to the host runtime.
`, allowedTools))
}

func promptExecutionEnabled(cfg PromptConfig) bool {
	if cfg.EnableExecutionGuidance == nil {
		return true
	}
	return *cfg.EnableExecutionGuidance
}

func promptScenarioEnabled(cfg PromptConfig) bool {
	if cfg.EnableScenarioGuidance == nil {
		return true
	}
	return *cfg.EnableScenarioGuidance
}
