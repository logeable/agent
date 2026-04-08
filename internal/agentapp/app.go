package agentapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/automation"
	"github.com/logeable/agent/pkg/codeexec"
	"github.com/logeable/agent/pkg/delegation"
	"github.com/logeable/agent/pkg/orchestration"
	"github.com/logeable/agent/pkg/profile"
)

type LoopOptions struct {
	ProfileName  string
	ProviderKind string
	ModelName    string
	BaseURL      string
	APIKey       string
	WorkDir      string
}

type Runtime struct {
	Config         *profile.Config
	Loop           *agent.Loop
	Events         *orchestration.EventBus
	Delegation     *delegation.LoopChildRunner
	Batch          *delegation.BatchRunner
	Automation     *automation.MemoryScheduler
	AutomationJobs *automation.MemoryJobStore
	AutomationRuns *automation.MemoryRunStore
	CodeExec       *codeexec.LocalSandbox
	CodeExecPrompt string
}

func BuildLoop(opts LoopOptions) (*agent.Loop, error) {
	cfg, err := loadConfig(opts)
	if err != nil {
		return nil, err
	}
	return cfg.BuildLoop(buildOptions(opts))
}

func BuildRuntime(opts LoopOptions) (*Runtime, error) {
	cfg, err := loadConfig(opts)
	if err != nil {
		return nil, err
	}
	loop, err := cfg.BuildLoop(buildOptions(opts))
	if err != nil {
		return nil, err
	}

	events := orchestration.NewEventBus()
	runtime := &Runtime{
		Config: cfg,
		Loop:   loop,
		Events: events,
	}

	loopFactory := func(ctx context.Context, spec delegation.ChildSpec) (*agent.Loop, error) {
		childCfg := *cfg
		childCfg.Agent = cfg.Agent
		childCfg.Provider = cfg.Provider
		childCfg.Tools = cfg.Tools
		if spec.MaxIterations > 0 {
			childCfg.Agent.MaxIterations = spec.MaxIterations
		}
		if len(spec.Tools) > 0 {
			childCfg.Tools.Enabled = append([]string(nil), spec.Tools...)
		}
		childOpts := profile.BuildOptions{
			ProviderKind: opts.ProviderKind,
			BaseURL:      opts.BaseURL,
			APIKey:       opts.APIKey,
			Model:        firstNonEmpty(spec.Model, opts.ModelName),
			WorkDir:      firstNonEmpty(spec.WorkDir, opts.WorkDir),
		}
		childLoop, err := childCfg.BuildLoop(childOpts)
		if err != nil {
			return nil, err
		}
		childPrompt := profile.BuildDelegationPrompt(profile.DelegationPromptInput{
			Goal:           spec.Goal,
			ContextSummary: spec.ContextSummary,
			WorkDir:        childOpts.WorkDir,
		})
		childLoop.Context.SystemPrompt = strings.TrimSpace(childLoop.Context.SystemPrompt + "\n\n---\n\n" + childPrompt)
		childLoop.Approval = loop.Approval
		return childLoop, nil
	}

	delegationPolicy := delegation.DefaultPolicy{
		MaxDepth:      cfg.Orchestration.Delegation.MaxDepth,
		MaxConcurrent: cfg.Orchestration.Delegation.MaxConcurrent,
		BlockedTools:  append([]string(nil), cfg.Orchestration.Delegation.BlockedTools...),
	}
	if cfg.Orchestration.Delegation.Enabled {
		maxConcurrent := cfg.Orchestration.Delegation.MaxConcurrent
		if maxConcurrent <= 0 {
			maxConcurrent = delegation.DefaultMaxConcurrentChildren
		}
		loop.Model = delegation.ToolCallLimiterModel{
			Inner:       loop.Model,
			MaxChildren: maxConcurrent,
		}
	}
	runtime.Delegation = &delegation.LoopChildRunner{
		Factory:  loopFactory,
		Policy:   delegationPolicy,
		Events:   events,
		Approval: loop.Approval,
	}
	runtime.Batch = &delegation.BatchRunner{
		Runner: runtime.Delegation,
		Policy: delegationPolicy,
	}
	if cfg.Orchestration.Delegation.Enabled {
		loop.Tools.Register(delegation.Tool{
			Runner:        runtime.Delegation,
			Batch:         runtime.Batch,
			MaxConcurrent: cfg.Orchestration.Delegation.MaxConcurrent,
			DefaultDepth:  1,
		})
	}

	jobStore := automation.NewMemoryJobStore()
	runStore := automation.NewMemoryRunStore()
	runtime.AutomationJobs = jobStore
	runtime.AutomationRuns = runStore
	jobRunner := automation.LoopJobRunner{
		RunStore: runStore,
		Events:   events,
		Factory: func(ctx context.Context, job automation.JobSpec) (func(context.Context, string, string) (string, error), error) {
			jobCfg := cfg
			if value := strings.TrimSpace(job.Profile); value != "" {
				path, err := ResolveProfileArgument(value)
				if err != nil {
					return nil, err
				}
				if path != "" {
					jobCfg, err = profile.Load(path)
					if err != nil {
						return nil, err
					}
				}
			}
			jobLoop, err := jobCfg.BuildLoop(profile.BuildOptions{
				ProviderKind: opts.ProviderKind,
				BaseURL:      opts.BaseURL,
				APIKey:       opts.APIKey,
				Model:        opts.ModelName,
				WorkDir:      opts.WorkDir,
			})
			if err != nil {
				return nil, err
			}
			jobLoop.Approval = loop.Approval
			wrappedPrompt := profile.BuildAutomationPrompt(profile.AutomationPromptInput{
				Task: job.Prompt,
			})
			return func(callCtx context.Context, sessionKey, prompt string) (string, error) {
				defer jobLoop.Close()
				return jobLoop.Process(callCtx, sessionKey, wrappedPrompt)
			}, nil
		},
	}
	runtime.Automation = automation.NewMemoryScheduler(jobStore, runStore, jobRunner, events)

	policy := codeexec.DefaultExecutionPolicy{
		MaxTimeout:     time.Duration(cfg.Orchestration.CodeExec.TimeoutMS) * time.Millisecond,
		MaxToolCalls:   cfg.Orchestration.CodeExec.MaxToolCalls,
		MaxStdoutBytes: cfg.Orchestration.CodeExec.MaxStdoutBytes,
		MaxStderrBytes: cfg.Orchestration.CodeExec.MaxStderrBytes,
	}
	runtime.CodeExec = &codeexec.LocalSandbox{
		Registry: loop.Tools,
		Policy:   policy,
		Events:   events,
	}
	runtime.CodeExecPrompt = profile.BuildCodeExecPrompt(profile.CodeExecPromptInput{
		AllowedTools: codeexec.AllowedToolNames(loop.Tools),
	})
	return runtime, nil
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	if r.Events != nil {
		r.Events.Close()
	}
	if r.Loop != nil {
		return r.Loop.Close()
	}
	return nil
}

func buildOptions(opts LoopOptions) profile.BuildOptions {
	return profile.BuildOptions{
		ProviderKind: opts.ProviderKind,
		BaseURL:      opts.BaseURL,
		APIKey:       opts.APIKey,
		Model:        opts.ModelName,
		WorkDir:      opts.WorkDir,
	}
}

func loadConfig(opts LoopOptions) (*profile.Config, error) {
	cfgPath, err := ResolveProfileArgument(opts.ProfileName)
	if err != nil {
		return nil, err
	}
	if cfgPath != "" {
		return profile.Load(cfgPath)
	}

	if _, err := os.Stat("agent.toml"); err == nil {
		return profile.Load("agent.toml")
	}

	return DefaultCLIProfile()
}

func ResolveProfileArgument(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not resolve home directory for profile lookup: %w", err)
	}
	return resolveProfileArgumentWithHome(value, homeDir), nil
}

func ResolveProfileArgumentWithHomeForTests(value, homeDir string) string {
	return resolveProfileArgumentWithHome(value, homeDir)
}

func resolveProfileArgumentWithHome(value, homeDir string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	if !filepath.IsAbs(trimmed) && !strings.ContainsRune(trimmed, filepath.Separator) {
		name := strings.TrimSuffix(trimmed, ".toml")
		if homeDir != "" {
			candidate := filepath.Join(homeDir, ".agentcli", name+".toml")
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}

	return trimmed
}

func DisplayProfileName(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) || strings.ContainsRune(value, filepath.Separator) {
		base := filepath.Base(value)
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	return strings.TrimSuffix(value, ".toml")
}

func DefaultCLIProfile() (*profile.Config, error) {
	return &profile.Config{
		Name: "agentcli",
		Provider: profile.ProviderConfig{
			Kind: "openai",
		},
		Agent: profile.AgentConfig{
			ID:            "agentcli",
			Identity:      profile.BuildDefaultIdentity(),
			Soul:          profile.BuildDefaultSoul(),
			MaxIterations: agent.DefaultMaxIterations,
		},
		Tools: profile.ToolsConfig{
			Enabled: []string{"read_file", "edit_file", "write_file", "bash", "web_fetch", "delegate_task"},
			ReadFile: profile.ReadFileToolConfig{
				MaxBytes: 32 * 1024,
			},
			Bash: profile.BashToolConfig{
				TimeoutMS:      30_000,
				MaxOutputBytes: 64 * 1024,
			},
			WebFetch: profile.WebFetchToolConfig{
				TimeoutMS: 20_000,
				MaxBytes:  128 * 1024,
			},
		},
		Orchestration: profile.OrchestrationConfig{
			Delegation: profile.DelegationConfig{
				Enabled:              true,
				MaxDepth:             2,
				MaxConcurrent:        2,
				DefaultMaxIterations: agent.DefaultMaxIterations,
			},
			Automation: profile.AutomationConfig{
				DefaultIntervalMS: int64((5 * time.Minute) / time.Millisecond),
				DefaultTimeoutMS:  int64((2 * time.Minute) / time.Millisecond),
			},
			CodeExec: profile.CodeExecConfig{
				TimeoutMS:      int64((5 * time.Minute) / time.Millisecond),
				MaxToolCalls:   50,
				MaxStdoutBytes: 64 * 1024,
				MaxStderrBytes: 16 * 1024,
			},
		},
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
