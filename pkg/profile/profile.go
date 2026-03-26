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
	"github.com/logeable/agent/pkg/skills"
	builtintools "github.com/logeable/agent/pkg/tools"
	"github.com/pelletier/go-toml/v2"
)

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

// EnvironmentInfo carries the minimum runtime facts that should be surfaced in
// the generated system prompt.
//
// Why:
// The agent should know its operating boundary, but we want to keep these facts
// structured at build time rather than hard-coding runtime details into profile
// files.
type EnvironmentInfo struct {
	WorkDir       string
	FilesScope    string
	FileRoots     []string
	EnabledTools  []string
	MaxIterations int
}

// BuildSystemPrompt composes the final system prompt from identity, soul, and
// the current runtime environment.
//
// Why:
// This keeps prompt construction explicit and layered. Identity and Soul come
// from the profile, while Environment comes from the actual instantiated agent.
func BuildSystemPrompt(identity, soul string, env EnvironmentInfo, skillsSummary string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		identity = BuildDefaultIdentity()
	}

	soul = strings.TrimSpace(soul)
	if soul == "" {
		soul = BuildDefaultSoul()
	}

	scope := strings.TrimSpace(env.FilesScope)
	if scope == "" {
		scope = string(builtintools.PathScopeWorkspace)
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
		maxIterations = agent.DefaultMaxIterations
	}

	environment := strings.TrimSpace(fmt.Sprintf(`
# Environment
Current working directory: %s
File access scope: %s
Allowed file roots: %s
Enabled tools: %s
Max tool-call iterations per turn: %d
`, env.WorkDir, scope, fileRoots, enabledTools, maxIterations))

	parts := []string{
		"# Identity\n" + identity,
		"# Soul\n" + soul,
	}
	if strings.TrimSpace(skillsSummary) != "" {
		parts = append(parts, skillsSummary)
	}
	parts = append(parts, environment)

	return strings.Join(parts, "\n\n---\n\n")
}

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
	Skills   SkillsConfig   `toml:"skills"`
	Tools    ToolsConfig    `toml:"tools"`
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
	Identity      string `toml:"identity"`
	Soul          string `toml:"soul"`
	MaxIterations int    `toml:"max_iterations"`
}

// FilesConfig declares the shared file access policy for file tools.
type FilesConfig struct {
	Scope string   `toml:"scope"`
	Roots []string `toml:"roots"`
}

// SkillsConfig declares where the runtime should look for local skill packs.
type SkillsConfig struct {
	Enabled *bool    `toml:"enabled"`
	Roots   []string `toml:"roots"`
}

// ToolsConfig declares which built-in tools are enabled and how they are tuned.
type ToolsConfig struct {
	Enabled   []string            `toml:"enabled"`
	ReadFile  ReadFileToolConfig  `toml:"read_file"`
	WriteFile WriteFileToolConfig `toml:"write_file"`
	EditFile  EditFileToolConfig  `toml:"edit_file"`
	Bash      BashToolConfig      `toml:"bash"`
	WebFetch  WebFetchToolConfig  `toml:"web_fetch"`
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

type WebFetchToolConfig struct {
	TimeoutMS int64  `toml:"timeout_ms"`
	MaxBytes  int64  `toml:"max_bytes"`
	UserAgent string `toml:"user_agent"`
}

// BuildOptions lets callers override selected profile values at runtime.
//
// Why:
// A profile should define the default instance shape, but the CLI still needs a
// small escape hatch for one-off overrides such as a different API key or model.
type BuildOptions struct {
	ProviderKind string
	BaseURL      string
	APIKey       string
	Model        string
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
	if providerKind != "openai" && providerKind != "openai_response" {
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

	pathPolicy, err := c.buildPathPolicy(workDir)
	if err != nil {
		return nil, err
	}

	skillsSummary, err := c.buildSkillsSummary(workDir)
	if err != nil {
		return nil, err
	}

	systemPrompt := BuildSystemPrompt(c.Agent.Identity, c.Agent.Soul, EnvironmentInfo{
		WorkDir:       workDir,
		FilesScope:    string(pathPolicy.Scope),
		FileRoots:     append([]string(nil), pathPolicy.Roots...),
		EnabledTools:  c.enabledTools(),
		MaxIterations: c.Agent.MaxIterations,
	}, skillsSummary)

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
	kind := strings.ToLower(strings.TrimSpace(firstNonEmpty(opts.ProviderKind, c.Provider.Kind)))
	if kind == "" {
		kind = "openai"
	}
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

	switch kind {
	case "openai":
		model, err := provider.NewOpenAICompatModel(provider.OpenAICompatConfig{
			BaseURL: baseURL,
			APIKey:  apiKey,
			Model:   modelName,
		})
		if err != nil {
			return nil, "", err
		}
		return model, modelName, nil
	case "openai_response":
		model, err := provider.NewOpenAIResponseModel(provider.OpenAIResponseConfig{
			BaseURL: baseURL,
			APIKey:  apiKey,
			Model:   modelName,
		})
		if err != nil {
			return nil, "", err
		}
		return model, modelName, nil
	default:
		return nil, "", fmt.Errorf("unsupported provider kind %q", c.Provider.Kind)
	}
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
				Timeout:        time.Duration(positiveInt64OrDefault(c.Tools.Bash.TimeoutMS, 60_000)) * time.Millisecond,
				MaxOutputBytes: positiveIntOrDefault(c.Tools.Bash.MaxOutputBytes, 64*1024),
				Shell:          strings.TrimSpace(c.Tools.Bash.Shell),
			})
		case "web_fetch":
			registry.Register(builtintools.WebFetchTool{
				Timeout:   time.Duration(positiveInt64OrDefault(c.Tools.WebFetch.TimeoutMS, 20_000)) * time.Millisecond,
				MaxBytes:  positiveInt64OrDefault(c.Tools.WebFetch.MaxBytes, 128*1024),
				UserAgent: strings.TrimSpace(c.Tools.WebFetch.UserAgent),
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

func (c *Config) buildSkillsSummary(workDir string) (string, error) {
	if c.Skills.Enabled != nil && !*c.Skills.Enabled {
		return "", nil
	}

	roots, err := c.resolvedSkillRoots(workDir)
	if err != nil {
		return "", err
	}

	found, err := skills.Load(roots)
	if err != nil {
		return "", err
	}
	return skills.BuildSummary(found), nil
}

func (c *Config) resolvedSkillRoots(workDir string) ([]string, error) {
	roots := c.Skills.Roots
	if len(roots) == 0 {
		return []string{filepath.Join(workDir, "skills")}, nil
	}

	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if !filepath.IsAbs(root) {
			root = filepath.Join(workDir, root)
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("could not resolve skill root %q: %w", root, err)
		}
		out = append(out, absRoot)
	}
	return out, nil
}

func (c *Config) enabledTools() []string {
	if len(c.Tools.Enabled) == 0 {
		return []string{"read_file", "edit_file", "write_file", "bash", "web_fetch"}
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
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not resolve working directory: %w", err)
	}
	return cwd, nil
}

func (c *Config) resolvedFileRoots() ([]string, error) {
	if len(c.Files.Roots) == 0 {
		return nil, fmt.Errorf("files.scope=explicit requires at least one root")
	}

	baseDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("could not resolve working directory: %w", err)
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
	case "read_file", "edit_file", "write_file", "bash", "web_fetch":
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
