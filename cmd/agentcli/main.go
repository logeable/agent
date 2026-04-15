package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/logeable/agent/internal/agentapp"
	"github.com/logeable/agent/internal/agentclirun"
	"github.com/logeable/agent/internal/agentclitui"
	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/agentcore/provider"
	"github.com/logeable/agent/pkg/orchestration"
	"github.com/logeable/agent/pkg/profile"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// This command is the smallest runnable terminal application for the extracted
// agent runtime using a real model provider.
//
// Why:
// This repository is focused on a stable execution core instead of demo-only
// scaffolding. The CLI therefore wires together:
// - the extracted Loop
// - the in-memory session store
// - the built-in file, shell, and web tools
// - a real OpenAI-compatible model provider
func main() {
	cmd := newRootCommand(os.Stdin, os.Stdout, os.Stderr)
	cmd.SetArgs(normalizeLegacyLongFlags(os.Args[1:]))
	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		code := 1
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		os.Exit(code)
	}
}

func normalizeLegacyLongFlags(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--"):
			out = append(out, arg)
		case strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "---") && len(arg) > 2 && arg[2] != '=':
			out = append(out, "-"+arg)
		default:
			out = append(out, arg)
		}
	}
	return out
}

type rootOptions struct {
	message          string
	sessionKey       string
	profileName      string
	providerKind     string
	modelName        string
	baseURL          string
	apiKey           string
	stream           bool
	showReasoning    bool
	showReasoningSet bool
	showEvents       bool
	showOrchEvents   bool
	useRuntime       bool
	delegateHint     bool
	delegateRequired bool
	autoApprove      bool
	renderMarkdown   bool
}

type modelsOptions struct {
	profileName  string
	providerKind string
	baseURL      string
	apiKey       string
}

type boolTrackingValue struct {
	value *bool
	set   *bool
}

func (v *boolTrackingValue) String() string {
	if v == nil || v.value == nil {
		return "false"
	}
	return strconv.FormatBool(*v.value)
}

func (v *boolTrackingValue) Set(raw string) error {
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return err
	}
	if v.value != nil {
		*v.value = parsed
	}
	if v.set != nil {
		*v.set = true
	}
	return nil
}

func (v *boolTrackingValue) Type() string {
	return "bool"
}

func newRootCommand(stdin *os.File, stdout, stderr io.Writer) *cobra.Command {
	opts := rootOptions{
		sessionKey:     "agentcli:default",
		profileName:    "agent",
		stream:         true,
		renderMarkdown: true,
	}

	cmd := &cobra.Command{
		Use:           "agentcli",
		Short:         "Run the local agent in CLI or TUI mode",
		SilenceUsage:  true,
		SilenceErrors: true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRootCommand(stdin, stdout, opts)
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	flags := cmd.Flags()
	flags.StringVarP(&opts.message, "message", "m", "", "Process a single message and exit")
	flags.StringVar(&opts.sessionKey, "session", opts.sessionKey, "Session key used to preserve conversation state")
	flags.StringVar(&opts.profileName, "profile", opts.profileName, "Profile name or path")
	flags.StringVar(&opts.providerKind, "provider", "", "Provider kind to use: openai, openai_response, or ollama")
	flags.StringVar(&opts.modelName, "model", "", "Model name for the selected provider")
	flags.StringVar(&opts.baseURL, "base-url", "", "Base URL for the selected provider")
	flags.StringVar(&opts.apiKey, "api-key", "", "API key for the selected provider")
	flags.BoolVar(&opts.stream, "stream", opts.stream, "Render model delta events when the provider supports streaming")
	flags.Var(&boolTrackingValue{value: &opts.showReasoning, set: &opts.showReasoningSet}, "show-reasoning", "Show streamed reasoning when the provider emits it")
	flags.BoolVar(&opts.showEvents, "events", false, "Show key runtime events")
	flags.BoolVar(&opts.showOrchEvents, "show-orchestration-events", false, "Show orchestration runtime events such as child delegation lifecycle")
	flags.BoolVar(&opts.useRuntime, "runtime", false, "Build the orchestration runtime instead of the plain loop")
	flags.BoolVar(&opts.delegateHint, "delegate-hint", false, "Append a delegation-planning hint for this runtime execution without hardcoding specific subtasks")
	flags.BoolVar(&opts.delegateRequired, "delegate-required", false, "Require this run to use delegate_task at least once; injects a strong temporary delegation system prompt")
	flags.BoolVar(&opts.autoApprove, "auto-approve", false, "Automatically approve tool approval requests")
	flags.BoolVar(&opts.renderMarkdown, "render-markdown", opts.renderMarkdown, "Render assistant messages as Markdown in terminal views")
	mustMarkNoOptDefVal(flags, "show-reasoning", "true")

	modelsCmd := newModelsCommand(stdout, stderr)
	cmd.AddCommand(modelsCmd)

	return cmd
}

func newModelsCommand(stdout, stderr io.Writer) *cobra.Command {
	opts := modelsOptions{
		profileName: "agent",
	}

	cmd := &cobra.Command{
		Use:           "models",
		Short:         "List models available from the selected provider",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelsCommand(stdout, opts)
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	flags := cmd.Flags()
	flags.StringVar(&opts.profileName, "profile", opts.profileName, "Profile name or path")
	flags.StringVar(&opts.providerKind, "provider", "", "Provider kind to use: openai, openai_response, or ollama")
	flags.StringVar(&opts.baseURL, "base-url", "", "Base URL for the selected provider")
	flags.StringVar(&opts.apiKey, "api-key", "", "API key for the selected provider")

	return cmd
}

func mustMarkNoOptDefVal(flags *pflag.FlagSet, name, value string) {
	flag := flags.Lookup(name)
	if flag == nil {
		panic(fmt.Sprintf("flag %q not found", name))
	}
	flag.NoOptDefVal = value
}

func runRootCommand(stdin *os.File, stdout io.Writer, opts rootOptions) error {
	loopOpts := agentapp.LoopOptions{
		ProfileName:  opts.profileName,
		ProviderKind: opts.providerKind,
		ModelName:    opts.modelName,
		BaseURL:      opts.baseURL,
		APIKey:       opts.apiKey,
	}
	var (
		loop    *agent.Loop
		runtime *agentapp.Runtime
		err     error
	)
	if opts.useRuntime || opts.delegateHint || opts.delegateRequired {
		runtime, err = agentapp.BuildRuntime(loopOpts)
		if err != nil {
			return err
		}
		defer runtime.Close()
		loop = runtime.Loop
	} else {
		loop, err = agentapp.BuildLoop(loopOpts)
		if err != nil {
			return err
		}
		defer loop.Close()
	}
	loop.DisableStreaming = !opts.stream
	loop.Events = agent.NewEventBus()
	defer loop.Events.Close()

	stdinMessage, err := readMessageFromStdin(stdin)
	if err != nil {
		return err
	}
	message := opts.message
	if strings.TrimSpace(stdinMessage) != "" {
		if strings.TrimSpace(message) != "" {
			message = strings.TrimSpace(message) + "\n\n" + strings.TrimSpace(stdinMessage)
		} else {
			message = stdinMessage
		}
	}
	if opts.delegateHint {
		message = buildDelegationHintMessage(message)
	}
	if opts.delegateRequired {
		loop.Context.SystemPrompt = appendTemporarySystemPrompt(loop.Context.SystemPrompt, buildDelegationRequiredPrompt())
	}

	if strings.TrimSpace(message) != "" {
		showReasoning := opts.showReasoning
		if !opts.showReasoningSet {
			showReasoning = false
		}
		var orchestrationEvents *orchestration.EventBus
		if runtime != nil {
			orchestrationEvents = runtime.Events
		}
		exitCode, err := agentclirun.RunSingleMessage(loop, orchestrationEvents, agentclirun.SingleRunOptions{
			SessionKey:              opts.sessionKey,
			Message:                 message,
			Stream:                  opts.stream,
			ShowReasoning:           showReasoning,
			ShowEvents:              opts.showEvents,
			ShowOrchestrationEvents: opts.showOrchEvents,
			AutoApprove:             opts.autoApprove,
			RenderMarkdown:          opts.renderMarkdown,
		})
		if err != nil {
			if exitCode == 0 {
				exitCode = 1
			}
			return &cliExitError{err: err, code: exitCode}
		}
		return nil
	}

	showReasoning := opts.showReasoning
	if !opts.showReasoningSet {
		showReasoning = true
	}

	if err := agentclitui.Run(loop, agentclitui.Options{
		SessionKey:     opts.sessionKey,
		ProfileName:    agentapp.DisplayProfileName(opts.profileName),
		Stream:         opts.stream,
		ShowReasoning:  showReasoning,
		ShowEvents:     opts.showEvents,
		AutoApprove:    opts.autoApprove,
		RenderMarkdown: opts.renderMarkdown,
	}); err != nil {
		return err
	}
	return nil
}

func runModelsCommand(stdout io.Writer, opts modelsOptions) error {
	cfg, err := loadCLIProfile(opts.profileName)
	if err != nil {
		return err
	}
	resolved, err := cfg.ResolveProvider(profile.BuildOptions{
		ProviderKind: opts.providerKind,
		BaseURL:      opts.baseURL,
		APIKey:       opts.apiKey,
	})
	if err != nil {
		return err
	}
	if resolved.Kind != "ollama" && strings.TrimSpace(resolved.APIKey) == "" {
		return fmt.Errorf("provider API key is required")
	}

	models, err := provider.ListModels(context.Background(), provider.ModelCatalogConfig{
		Kind:    resolved.Kind,
		BaseURL: resolved.BaseURL,
		APIKey:  resolved.APIKey,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "provider: %s\n", resolved.Kind)
	fmt.Fprintf(stdout, "base_url: %s\n", resolved.BaseURL)
	fmt.Fprintf(stdout, "models: %d\n", len(models))
	for _, model := range models {
		if model.OwnedBy != "" {
			fmt.Fprintf(stdout, "- %s\t%s\n", model.ID, model.OwnedBy)
			continue
		}
		fmt.Fprintf(stdout, "- %s\n", model.ID)
	}
	return nil
}

type cliExitError struct {
	err  error
	code int
}

func (e *cliExitError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *cliExitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *cliExitError) ExitCode() int {
	if e == nil || e.code == 0 {
		return 1
	}
	return e.code
}

func buildDelegationHintMessage(base string) string {
	hint := strings.TrimSpace(`
Delegation hint for this run:
If the task is complex enough to benefit from subagents, first form a short delegation plan before calling delegate_task.
Only delegate isolated reasoning-heavy subtasks or independent workstreams.
When delegating, include concrete paths, errors, constraints, and any other missing context the child needs because child workers cannot see the parent conversation history.
If the task does not benefit from delegation, continue normally without using delegate_task.
`)
	if strings.TrimSpace(base) == "" {
		return hint
	}
	return strings.TrimSpace(base) + "\n\n" + hint
}

func buildDelegationRequiredPrompt() string {
	return strings.TrimSpace(`
# Delegation Required
This run must use delegate_task at least once before you deliver the final answer.
First form a short delegation plan, then call delegate_task for at least one isolated reasoning-heavy subtask or one batch of independent subtasks.
Do not complete the entire task yourself without delegating.
When delegating, pass concrete paths, errors, constraints, and any other missing context because child workers cannot see the parent conversation history.
After delegated work returns, synthesize the child summaries into the final response.
`)
}

func appendTemporarySystemPrompt(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + "\n\n---\n\n" + extra
	}
}

func readMessageFromStdin(stdin *os.File) (string, error) {
	if stdin == nil {
		return "", nil
	}

	info, err := stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("could not inspect stdin: %w", err)
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}

	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("could not read stdin: %w", err)
	}
	return string(data), nil
}

func resolveProfileArgument(raw string) (string, error) {
	return agentapp.ResolveProfileArgument(raw)
}

func resolveProfileArgumentWithHome(value, homeDir string) string {
	return agentapp.ResolveProfileArgumentWithHomeForTests(value, homeDir)
}

func loadCLIProfile(profileName string) (*profile.Config, error) {
	cfgPath, err := resolveProfileArgument(profileName)
	if err != nil {
		return nil, err
	}
	if cfgPath != "" {
		return profile.Load(cfgPath)
	}
	if _, err := os.Stat("agent.toml"); err == nil {
		return profile.Load("agent.toml")
	}
	return agentapp.DefaultCLIProfile()
}
