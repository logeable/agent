package mcpbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/logeable/agent/pkg/agentcore/tooling"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config declares how MCP servers should be connected for one agent instance.
//
// Why:
// MCP is not part of the execution core itself. It is an external capability
// source. This config therefore belongs to the bridge layer that translates
// MCP servers into normal tooling.Tool values.
type Config struct {
	Enabled              *bool                   `toml:"enabled"`
	MaxToolResponseChars int                     `toml:"max_tool_response_chars"`
	Servers              map[string]ServerConfig `toml:"servers"`
}

// ServerConfig describes one MCP server connection. The first version keeps a
// deliberately narrow scope and only supports stdio transport.
type ServerConfig struct {
	Enabled *bool             `toml:"enabled"`
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	EnvFile string            `toml:"env_file"`
}

type Connection struct {
	Name    string
	Client  *mcp.Client
	Session *mcp.ClientSession
	Tools   []*mcp.Tool
}

// Manager owns MCP client sessions and can expose them as normal runtime tools.
type Manager struct {
	mu                   sync.Mutex
	servers              map[string]*Connection
	maxToolResponseChars int
}

func NewManager(maxToolResponseChars int) *Manager {
	return &Manager{
		servers:              make(map[string]*Connection),
		maxToolResponseChars: maxToolResponseChars,
	}
}

// LoadStdioServers connects every enabled stdio server and lists its tools.
func (m *Manager) LoadStdioServers(ctx context.Context, cfg Config, workDir string) error {
	if cfg.Enabled != nil && !*cfg.Enabled {
		return nil
	}

	for name, server := range cfg.Servers {
		if server.Enabled != nil && !*server.Enabled {
			continue
		}
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("mcp server %q requires command", name)
		}
		conn, err := connectStdioServer(ctx, name, server, workDir)
		if err != nil {
			return fmt.Errorf("connect mcp server %q: %w", name, err)
		}
		m.mu.Lock()
		m.servers[name] = conn
		m.mu.Unlock()
	}

	return nil
}

func connectStdioServer(ctx context.Context, name string, cfg ServerConfig, workDir string) (*Connection, error) {
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "agent",
		Version: "0.1.0",
	}, nil)

	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Dir = workDir

	envMap := make(map[string]string)
	for _, item := range cmd.Environ() {
		if idx := strings.Index(item, "="); idx > 0 {
			envMap[item[:idx]] = item[idx+1:]
		}
	}

	if strings.TrimSpace(cfg.EnvFile) != "" {
		envFile := cfg.EnvFile
		if !filepath.IsAbs(envFile) {
			envFile = filepath.Join(workDir, envFile)
		}
		loaded, err := loadEnvFile(envFile)
		if err != nil {
			return nil, err
		}
		for key, value := range loaded {
			envMap[key] = value
		}
	}
	for key, value := range cfg.Env {
		envMap[key] = value
	}
	if len(envMap) > 0 {
		env := make([]string, 0, len(envMap))
		for key, value := range envMap {
			env = append(env, key+"="+value)
		}
		cmd.Env = env
	}

	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, err
	}

	var tools []*mcp.Tool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}

	return &Connection{
		Name:    name,
		Client:  client,
		Session: session,
		Tools:   tools,
	}, nil
}

// Tools converts every MCP server tool into the local runtime Tool interface.
func (m *Manager) Tools() []tooling.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]tooling.Tool, 0)
	for serverName, conn := range m.servers {
		for _, tool := range conn.Tools {
			out = append(out, NewTool(m, serverName, tool))
		}
	}
	return out
}

func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (*mcp.CallToolResult, error) {
	m.mu.Lock()
	conn, ok := m.servers[serverName]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("mcp server %q not found", serverName)
	}

	return conn.Session.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
}

func (m *Manager) MaxToolResponseChars() int {
	if m == nil {
		return 0
	}
	return m.maxToolResponseChars
}

func (m *Manager) Close() error {
	m.mu.Lock()
	servers := m.servers
	m.servers = make(map[string]*Connection)
	m.mu.Unlock()

	var firstErr error
	for _, conn := range servers {
		if err := conn.Session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func loadEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open env file %q: %w", path, err)
	}
	defer file.Close()

	vars := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid env line %q", line)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, `"'`)
		vars[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan env file %q: %w", path, err)
	}
	return vars, nil
}

func schemaAsMap(schema any) map[string]any {
	if schema == nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	if direct, ok := schema.(map[string]any); ok {
		return direct
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	return out
}

func resultText(content []mcp.Content) string {
	parts := make([]string, 0, len(content))
	for _, item := range content {
		switch value := item.(type) {
		case *mcp.TextContent:
			parts = append(parts, value.Text)
		default:
			parts = append(parts, fmt.Sprintf("[%T]", value))
		}
	}
	return strings.Join(parts, "\n")
}
