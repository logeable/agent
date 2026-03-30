package agentacp

import "encoding/json"

const protocolVersion = 1

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeRequest struct {
	ProtocolVersion int `json:"protocolVersion"`
}

type initializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities agentCapabilities `json:"agentCapabilities"`
	AgentInfo         implementation    `json:"agentInfo"`
	AuthMethods       []any             `json:"authMethods"`
}

type agentCapabilities struct {
	LoadSession         bool                `json:"loadSession"`
	PromptCapabilities  promptCapabilities  `json:"promptCapabilities"`
	MCPCapabilities     mcpCapabilities     `json:"mcpCapabilities"`
	SessionCapabilities sessionCapabilities `json:"sessionCapabilities"`
}

type promptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

type mcpCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

type sessionCapabilities struct{}

type implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type newSessionRequest struct {
	Cwd        string         `json:"cwd"`
	MCPServers []acpMCPServer `json:"mcpServers"`
}

type newSessionResponse struct {
	SessionID string `json:"sessionId"`
}

type promptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

type promptResponse struct {
	StopReason string `json:"stopReason"`
}

type cancelNotification struct {
	SessionID string `json:"sessionId"`
}

type sessionNotification struct {
	SessionID string `json:"sessionId"`
	Update    any    `json:"update"`
}

type contentBlock struct {
	Type        string            `json:"type"`
	Text        string            `json:"text,omitempty"`
	URI         string            `json:"uri,omitempty"`
	Name        string            `json:"name,omitempty"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Resource    *embeddedResource `json:"resource,omitempty"`
}

type embeddedResource struct {
	Resource embeddedResourceContents `json:"resource"`
}

type embeddedResourceContents struct {
	URI      string `json:"uri"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type content struct {
	Content contentBlock `json:"content"`
}

type contentChunkUpdate struct {
	SessionUpdate string  `json:"sessionUpdate"`
	Content       content `json:"content"`
}

type toolCallUpdateNotification struct {
	SessionUpdate string            `json:"sessionUpdate"`
	ToolCallID    string            `json:"toolCallId"`
	Title         string            `json:"title,omitempty"`
	Kind          string            `json:"kind,omitempty"`
	Status        string            `json:"status,omitempty"`
	RawInput      map[string]any    `json:"rawInput,omitempty"`
	RawOutput     map[string]any    `json:"rawOutput,omitempty"`
	Content       []toolCallContent `json:"content,omitempty"`
}

type toolCallContent struct {
	Type    string  `json:"type"`
	Content content `json:"content"`
}

type sessionInfoUpdate struct {
	SessionUpdate string  `json:"sessionUpdate"`
	Title         *string `json:"title,omitempty"`
	UpdatedAt     string  `json:"updatedAt,omitempty"`
}

type acpMCPServer struct {
	Type    string            `json:"type"`
	Name    string            `json:"name"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers []acpHTTPHeader   `json:"headers,omitempty"`
}

type acpHTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
