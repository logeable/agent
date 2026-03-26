package agentclitui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/logeable/agent/internal/agentclirun"
	"github.com/logeable/agent/pkg/agentcore/agent"
	"github.com/logeable/agent/pkg/agentcore/tooling"
)

// Options controls the interactive TUI behavior without leaking TUI details
// into cmd/agentcli.
type Options struct {
	SessionKey  string
	Stream      bool
	ShowEvents  bool
	AutoApprove bool
}

type processDoneMsg struct {
	response string
	err      error
}

type runtimeEventMsg struct {
	event agent.Event
}

type approvalPromptMsg struct {
	request tooling.ApprovalRequest
	reply   chan bool
}

type model struct {
	loop       *agent.Loop
	sessionKey string
	stream     bool
	showEvents bool

	input          textinput.Model
	messageView    viewport.Model
	eventsView     viewport.Model
	width          int
	height         int
	headerHeight   int
	footerHeight   int
	approvalHeight int

	status        string
	busy          bool
	streamingSeen bool

	awaitingApproval bool
	approvalRequest  tooling.ApprovalRequest
	approvalReply    chan bool

	transcript string
	eventLog   string
}

// Run starts the interactive TUI for agentcli. The CLI entrypoint stays small
// while this package owns the Bubble Tea specific state machine.
func Run(loop *agent.Loop, opts Options) error {
	m := newModel(loop, opts)
	program := tea.NewProgram(m, tea.WithAltScreen())

	loop.Approval = func(ctx context.Context, req tooling.ApprovalRequest) (bool, error) {
		if opts.AutoApprove {
			program.Send(runtimeEventMsg{
				event: agent.Event{
					Kind: agent.EventApprovalResolved,
					Payload: agent.ApprovalResolvedPayload{
						Tool:      req.Tool,
						RequestID: req.ID,
						Approved:  true,
						Reason:    "auto-approved by CLI flag",
					},
				},
			})
			return true, nil
		}

		reply := make(chan bool, 1)
		program.Send(approvalPromptMsg{request: req, reply: reply})
		select {
		case approved, ok := <-reply:
			if !ok {
				return false, context.Canceled
			}
			return approved, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}

	sub := loop.Events.Subscribe(128)
	defer loop.Events.Unsubscribe(sub.ID)
	go func() {
		for evt := range sub.C {
			program.Send(runtimeEventMsg{event: evt})
		}
	}()

	_, err := program.Run()
	return err
}

func newModel(loop *agent.Loop, opts Options) model {
	input := textinput.New()
	input.Placeholder = "Send a message"
	input.Focus()
	input.CharLimit = 0
	input.Prompt = "> "

	messageView := viewport.New(0, 0)
	eventsView := viewport.New(0, 0)

	m := model{
		loop:           loop,
		sessionKey:     opts.SessionKey,
		stream:         opts.Stream,
		showEvents:     opts.ShowEvents,
		input:          input,
		messageView:    messageView,
		eventsView:     eventsView,
		status:         "Ready",
		headerHeight:   3,
		footerHeight:   2,
		approvalHeight: 0,
	}
	m.appendMessage("agentcli")
	m.appendMessage("Enter to send, Ctrl+C to quit.")
	m.appendMessage("")
	return m
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.Width = maxInt(10, msg.Width-4)
		m.refreshLayout()
		return m, nil
	case runtimeEventMsg:
		m.handleRuntimeEvent(msg.event)
		return m, nil
	case processDoneMsg:
		m.busy = false
		if msg.err != nil {
			switch err := msg.err.(type) {
			case *agent.ApprovalDeniedError:
				m.status = "Approval denied"
				m.appendMessage("approval denied: " + agentclirun.FormatDeniedError(err))
			case *agent.ApprovalRequiredError:
				m.status = "Approval required"
				m.appendMessage("approval required: " + agentclirun.FormatApprovalError(err))
			default:
				m.status = "Error"
				m.appendMessage("error: " + msg.err.Error())
			}
			m.streamingSeen = false
			return m, nil
		}
		if !m.streamingSeen && strings.TrimSpace(msg.response) != "" {
			m.appendMessage(msg.response)
		} else if m.streamingSeen {
			m.ensureTrailingNewline()
		}
		m.streamingSeen = false
		m.status = "Ready"
		return m, nil
	case approvalPromptMsg:
		m.awaitingApproval = true
		m.approvalRequest = msg.request
		m.approvalReply = msg.reply
		m.status = "Waiting for approval"
		m.refreshLayout()
		return m, nil
	case tea.KeyMsg:
		if m.awaitingApproval {
			switch strings.ToLower(msg.String()) {
			case "y":
				m.approvalReply <- true
				close(m.approvalReply)
				m.awaitingApproval = false
				m.approvalReply = nil
				m.status = "Approval granted"
				m.refreshLayout()
				return m, nil
			case "n", "esc":
				m.approvalReply <- false
				close(m.approvalReply)
				m.awaitingApproval = false
				m.approvalReply = nil
				m.status = "Approval denied"
				m.refreshLayout()
				return m, nil
			default:
				return m, nil
			}
		}

		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEnter:
			if m.busy {
				return m, nil
			}
			line := strings.TrimSpace(m.input.Value())
			if line == "" {
				return m, nil
			}
			m.input.SetValue("")
			m.busy = true
			m.streamingSeen = false
			m.status = "Running"
			m.appendMessage("> " + line)
			return m, m.processMessageCmd(line)
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return "\nLoading..."
	}

	mainPanel := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(0, 1).
		Width(maxInt(20, m.messageView.Width+2)).
		Height(m.messageView.Height + 2).
		Render(m.messageView.View())

	body := mainPanel
	if m.showEvents {
		eventsPanel := lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1).
			Width(maxInt(20, m.eventsView.Width+2)).
			Height(m.eventsView.Height + 2).
			Render(m.eventsView.View())
		body = lipgloss.JoinHorizontal(lipgloss.Top, mainPanel, eventsPanel)
	}

	sections := []string{
		m.headerView(),
		body,
	}
	if m.awaitingApproval {
		sections = append(sections, m.approvalView())
	}
	sections = append(sections, m.inputView(), m.statusView())
	return strings.Join(sections, "\n")
}

func (m model) processMessageCmd(message string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.loop.Process(context.Background(), m.sessionKey, message)
		return processDoneMsg{response: resp, err: err}
	}
}

func (m *model) handleRuntimeEvent(evt agent.Event) {
	switch evt.Kind {
	case agent.EventModelDelta:
		if !m.stream {
			return
		}
		payload, ok := evt.Payload.(agent.ModelDeltaPayload)
		if !ok || payload.Delta == "" {
			return
		}
		m.transcript += payload.Delta
		m.messageView.SetContent(m.transcript)
		m.messageView.GotoBottom()
		m.streamingSeen = true
	default:
		if !m.showEvents {
			return
		}
		line := formatEventLine(evt)
		if line == "" {
			return
		}
		m.appendEvent(line)
	}
}

func (m *model) appendMessage(line string) {
	m.transcript += line + "\n"
	m.messageView.SetContent(m.transcript)
	m.messageView.GotoBottom()
}

func (m *model) appendEvent(line string) {
	m.eventLog += line + "\n"
	m.eventsView.SetContent(m.eventLog)
	m.eventsView.GotoBottom()
}

func (m *model) ensureTrailingNewline() {
	if m.transcript == "" || strings.HasSuffix(m.transcript, "\n") {
		return
	}
	m.transcript += "\n"
	m.messageView.SetContent(m.transcript)
	_ = m.messageView.GotoBottom()
}

func (m model) approvalView() string {
	lines := []string{"Approval required"}
	if m.approvalRequest.Tool != "" {
		lines = append(lines, "Tool: "+m.approvalRequest.Tool)
	}
	if m.approvalRequest.ActionLabel != "" {
		lines = append(lines, "Action: "+m.approvalRequest.ActionLabel)
	}
	if m.approvalRequest.Reason != "" {
		lines = append(lines, "Reason: "+m.approvalRequest.Reason)
	}
	if command, ok := m.approvalRequest.Details["command"].(string); ok && command != "" {
		lines = append(lines, "Command: "+command)
	}
	if path, ok := m.approvalRequest.Details["resolved_path"].(string); ok && path != "" {
		lines = append(lines, "Path: "+path)
	}
	lines = append(lines, "[y] approve  [n/esc] deny")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("214")).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}

func (m model) statusView() string {
	parts := []string{m.status}
	if m.busy {
		parts = append(parts, "busy")
	}
	if m.stream {
		parts = append(parts, "stream")
	}
	if m.showEvents {
		parts = append(parts, "events")
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("250")).
		Render(strings.Join(parts, " | "))
}

func (m model) headerView() string {
	left := "agentcli"
	rightParts := []string{
		"session=" + m.sessionKey,
	}
	if m.busy {
		rightParts = append(rightParts, "running")
	}
	right := strings.Join(rightParts, " | ")
	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("69")).Width(maxInt(10, m.width-lipgloss.Width(right)-1)).Render(left),
		lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(right),
	)
}

func (m model) inputView() string {
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("63")).
		Padding(0, 1).
		Render(m.input.View())
}

func (m *model) refreshLayout() {
	if m.width == 0 || m.height == 0 {
		return
	}

	m.approvalHeight = 0
	if m.awaitingApproval {
		m.approvalHeight = 6
	}

	bodyHeight := m.height - m.headerHeight - m.footerHeight - m.approvalHeight
	if bodyHeight < 8 {
		bodyHeight = 8
	}

	messageWidth := m.width
	eventsWidth := 0
	if m.showEvents {
		eventsWidth = maxInt(28, m.width/3)
		if eventsWidth > m.width/2 {
			eventsWidth = m.width / 2
		}
		messageWidth = m.width - eventsWidth
		if messageWidth < 40 {
			messageWidth = 40
			eventsWidth = maxInt(20, m.width-messageWidth)
		}
	}

	messageInnerWidth := maxInt(20, messageWidth-4)
	eventsInnerWidth := maxInt(16, eventsWidth-4)
	m.messageView.Width = messageInnerWidth
	m.messageView.Height = bodyHeight - 2
	m.messageView.SetContent(m.transcript)
	m.messageView.GotoBottom()

	m.eventsView.Width = eventsInnerWidth
	m.eventsView.Height = bodyHeight - 2
	m.eventsView.SetContent(m.eventLog)
	m.eventsView.GotoBottom()
}

func formatEventLine(evt agent.Event) string {
	return agentclirun.FormatEventLine(evt)
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
