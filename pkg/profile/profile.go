package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/agentcore/provider"
	"github.com/logeable/agent/pkg/agentcore/session"
	"github.com/logeable/agent/pkg/agentcore/tooling"
	builtintools "github.com/logeable/agent/pkg/tools"
	"github.com/pelletier/go-toml/v2"
)

// DefaultSystemPrompt is used when a profile does not provide its own prompt.
//
// Why:
// A profile should be able to omit boilerplate and still build a usable agent
// instance. Keeping the default here makes that behavior explicit and testable.
const DefaultSystemPrompt = "You are a focused coding agent. Use tools when needed, and keep answers short."

// Config is the top-level profile document.
//
// What:
// One profile describes one agent instance. It does not describe workflows,
// inheritance, or other higher-level orchestration concepts.
//
// Why:
// The goal of the first version is to keep profile files narrow and stable.
// They exist to declare instance parameters, not to become a full platform DSL.
type Config struct {
	Name     string         `toml:"name"`
	Provider ProviderConfig `toml:"provider"`
	Agent    AgentConfig    `toml:"agent"`
	Files    FilesConfig    `toml:"files"`
	Tools    ToolsConfig    `toml:"tools"`

	sourcePath string
}

// ProviderConfig declares how the agent talks to a model provider.
type ProviderConfig struct {
	Kind      string `toml:"kind"`
	BaseURL   string `toml:"base_url"`
	APIKey    string `toml:"api_key"`
	APIKeyEnv string `toml:"api_key_env"`
	Model     string `toml:"model"`
}

// AgentConfig declares instance-level runtime parameters.
type AgentConfig struct {
	ID            string `toml:"id"`
	SystemPrompt  string `toml:"system_prompt"`
	WorkDir       string `toml:"workdir"`
	MaxIterations int    `toml:"max_iterations"`
}

// FilesConfig declares the shared file access policy for file tools.
type FilesConfig struct {
	Scope string   `toml:"scope"`
	Roots []string `toml:"roots"`
}

// ToolsConfig declares which built-in tools are enabled and how they are tuned.
type ToolsConfig struct {
	Enabled   []string            `toml:"enabled"`
	ReadFile  ReadFileToolConfig  `toml:"read_file"`
	WriteFile WriteFileToolConfig `toml:"write_file"`
	EditFile  EditFileToolConfig  `toml:"edit_file"`
	Bash      BashToolConfig      `toml:"bash"`
}

type ReadFileToolConfig struct {
	MaxBytes int64 `toml:"max_bytes"`
}

type WriteFileToolConfig struct{}

type EditFileToolConfig struct{}

type BashToolConfig struct {
	TimeoutMS      int64  `toml:"timeout_ms"`
	MaxOutputBytes int    `toml:"max_output_bytes"`
	Shell          string `toml:"shell"`
}

// BuildOptions lets callers override selected profile values at runtime.
//
// Why:
// A profile should define the default instance shape, but the CLI still needs a
// small escape hatch for one-off overrides such as a different API key or model.
type BuildOptions struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Load reads and parses a TOML profile file.
func Load(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("profile path is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read profile %q: %w", path, err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("could not parse profile %q: %w", path, err)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("could not resolve profile path %q: %w", path, err)
	}
	cfg.sourcePath = absPath

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks whether the profile is internally coherent.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("profile is nil")
	}

	providerKind := strings.ToLower(strings.TrimSpace(c.Provider.Kind))
	if providerKind == "" {
		providerKind = "openai"
	}
	if providerKind != "openai" {
		return fmt.Errorf("unsupported provider kind %q", c.Provider.Kind)
	}

	for _, name := range c.enabledTools() {
		if !isSupportedTool(name) {
			return fmt.Errorf("unsupported tool %q", name)
		}
	}

	scope := strings.ToLower(strings.TrimSpace(c.Files.Scope))
	switch scope {
	case "", string(builtintools.PathScopeWorkspace), string(builtintools.PathScopeAny), string(builtintools.PathScopeExplicit):
	default:
		return fmt.Errorf("unsupported files.scope %q", c.Files.Scope)
	}
	if scope == string(builtintools.PathScopeExplicit) && len(c.Files.Roots) == 0 {
		return fmt.Errorf("files.scope=explicit requires at least one root")
	}
	if c.Agent.MaxIterations < 0 {
		return fmt.Errorf("agent.max_iterations must be zero or greater")
	}

	return nil
}

// BuildLoop constructs a runnable agent loop from the profile.
//
// What:
// This is the assembly step that turns a declarative profile into concrete
// runtime objects: provider, session store, tool registry, and loop.
//
// Why:
// Profile loading belongs above `agentcore`, but once parsed it should produce
// a normal `agent.Loop` with no special runtime path.
func (c *Config) BuildLoop(opts BuildOptions) (*agent.Loop, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	workDir, err := c.resolvedWorkDir()
	if err != nil {
		return nil, err
	}

	model, modelName, err := c.buildModel(opts)
	if err != nil {
		return nil, err
	}

	registry, err := c.buildRegistry(workDir)
	if err != nil {
		return nil, err
	}

	systemPrompt := strings.TrimSpace(c.Agent.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = DefaultSystemPrompt
	}

	return &agent.Loop{
		Model:         model,
		ModelName:     modelName,
		AgentID:       strings.TrimSpace(c.Agent.ID),
		Tools:         registry,
		Sessions:      session.NewMemoryStore(),
		MaxIterations: c.Agent.MaxIterations,
		Context: agent.ContextBuilder{
			SystemPrompt: systemPrompt,
		},
	}, nil
}

func (c *Config) buildModel(opts BuildOptions) (provider.ChatModel, string, error) {
	baseURL := firstNonEmpty(opts.BaseURL, c.Provider.BaseURL, os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1")
	apiKeyEnv := firstNonEmpty(c.Provider.APIKeyEnv, "OPENAI_API_KEY")
	apiKey := firstNonEmpty(opts.APIKey, c.Provider.APIKey, os.Getenv(apiKeyEnv))
	modelName := firstNonEmpty(opts.Model, c.Provider.Model, os.Getenv("OPENAI_MODEL"))

	if strings.TrimSpace(apiKey) == "" {
		return nil, "", fmt.Errorf("provider API key is required")
	}
	if strings.TrimSpace(modelName) == "" {
		return nil, "", fmt.Errorf("provider model is required")
	}

	model, err := provider.NewOpenAICompatModel(provider.OpenAICompatConfig{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   modelName,
	})
	if err != nil {
		return nil, "", err
	}
	return model, modelName, nil
}

func (c *Config) buildRegistry(workDir string) (*tooling.Registry, error) {
	registry := tooling.NewRegistry()
	pathPolicy, err := c.buildPathPolicy(workDir)
	if err != nil {
		return nil, err
	}

	for _, name := range c.enabledTools() {
		switch name {
		case "read_file":
			registry.Register(builtintools.ReadFileTool{
				PathPolicy: pathPolicy,
				MaxBytes:   positiveInt64OrDefault(c.Tools.ReadFile.MaxBytes, 128*1024),
			})
		case "write_file":
			registry.Register(builtintools.WriteFileTool{
				PathPolicy: pathPolicy,
			})
		case "edit_file":
			registry.Register(builtintools.EditFileTool{
				PathPolicy: pathPolicy,
			})
		case "bash":
			registry.Register(builtintools.BashTool{
				WorkDir:        workDir,
				Timeout:        time.Duration(positiveInt64OrDefault(c.Tools.Bash.TimeoutMS, 30_000)) * time.Millisecond,
				MaxOutputBytes: positiveIntOrDefault(c.Tools.Bash.MaxOutputBytes, 64*1024),
				Shell:          strings.TrimSpace(c.Tools.Bash.Shell),
			})
		default:
			return nil, fmt.Errorf("unsupported tool %q", name)
		}
	}

	return registry, nil
}

func (c *Config) buildPathPolicy(workDir string) (builtintools.PathPolicy, error) {
	scope := strings.ToLower(strings.TrimSpace(c.Files.Scope))
	if scope == "" {
		scope = string(builtintools.PathScopeWorkspace)
	}

	switch builtintools.PathScope(scope) {
	case builtintools.PathScopeWorkspace:
		return builtintools.PathPolicy{
			Scope: builtintools.PathScopeWorkspace,
			Roots: []string{workDir},
		}, nil
	case builtintools.PathScopeAny:
		return builtintools.PathPolicy{
			Scope: builtintools.PathScopeAny,
		}, nil
	case builtintools.PathScopeExplicit:
		roots, err := c.resolvedFileRoots()
		if err != nil {
			return builtintools.PathPolicy{}, err
		}
		return builtintools.PathPolicy{
			Scope: builtintools.PathScopeExplicit,
			Roots: roots,
		}, nil
	default:
		return builtintools.PathPolicy{}, fmt.Errorf("unsupported files.scope %q", c.Files.Scope)
	}
}

func (c *Config) enabledTools() []string {
	if len(c.Tools.Enabled) == 0 {
		return []string{"read_file", "edit_file", "write_file", "bash"}
	}

	enabled := make([]string, 0, len(c.Tools.Enabled))
	seen := make(map[string]struct{}, len(c.Tools.Enabled))
	for _, name := range c.Tools.Enabled {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		enabled = append(enabled, name)
		seen[name] = struct{}{}
	}
	return enabled
}

func (c *Config) resolvedWorkDir() (string, error) {
	workDir := strings.TrimSpace(c.Agent.WorkDir)
	if workDir == "" {
		workDir = "."
	}

	baseDir := ""
	if c.sourcePath != "" {
		baseDir = filepath.Dir(c.sourcePath)
	}
	if baseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("could not resolve working directory: %w", err)
		}
		baseDir = cwd
	}

	if !filepath.IsAbs(workDir) {
		workDir = filepath.Join(baseDir, workDir)
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("could not resolve profile workdir %q: %w", workDir, err)
	}
	return absWorkDir, nil
}

func (c *Config) resolvedFileRoots() ([]string, error) {
	if len(c.Files.Roots) == 0 {
		return nil, fmt.Errorf("files.scope=explicit requires at least one root")
	}

	baseDir := ""
	if c.sourcePath != "" {
		baseDir = filepath.Dir(c.sourcePath)
	}
	if baseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("could not resolve working directory: %w", err)
		}
		baseDir = cwd
	}

	roots := make([]string, 0, len(c.Files.Roots))
	for _, root := range c.Files.Roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if !filepath.IsAbs(root) {
			root = filepath.Join(baseDir, root)
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("could not resolve files root %q: %w", root, err)
		}
		roots = append(roots, absRoot)
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("files.scope=explicit requires at least one non-empty root")
	}
	return roots, nil
}

func isSupportedTool(name string) bool {
	switch name {
	case "read_file", "edit_file", "write_file", "bash":
		return true
	default:
		return false
	}
}

func positiveInt64OrDefault(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

func positiveIntOrDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
