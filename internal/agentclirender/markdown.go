package agentclirender

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
)

var markdownRenderers sync.Map

// RenderMarkdown renders assistant markdown for terminal display.
//
// If rendering fails, the original content is returned unchanged so terminal
// output remains robust even when the renderer cannot initialize.
func RenderMarkdown(content string, width int) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}

	renderer, err := markdownRenderer(width)
	if err != nil {
		return content
	}

	rendered, err := renderer.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimRight(rendered, "\n")
}

func markdownRenderer(width int) (*glamour.TermRenderer, error) {
	if width < 0 {
		width = 0
	}
	if cached, ok := markdownRenderers.Load(width); ok {
		if renderer, ok := cached.(*glamour.TermRenderer); ok {
			return renderer, nil
		}
	}

	options := []glamour.TermRendererOption{
		glamour.WithAutoStyle(),
	}
	if width > 0 {
		options = append(options, glamour.WithWordWrap(width))
	}
	renderer, err := glamour.NewTermRenderer(options...)
	if err != nil {
		return nil, err
	}
	markdownRenderers.Store(width, renderer)
	return renderer, nil
}
