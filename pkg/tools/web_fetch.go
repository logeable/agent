package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/logeable/agent/pkg/agentcore/tooling"
)

// WebFetchTool fetches a URL over HTTP(S) and returns bounded text content.
//
// What:
// The caller provides a URL and the tool performs a GET request.
//
// Why:
// Agents often need a minimal way to inspect web content without turning web
// access into a much larger browsing subsystem. This tool is intentionally
// narrow: one request, bounded output, text-focused responses.
type WebFetchTool struct {
	Timeout   time.Duration
	MaxBytes  int64
	UserAgent string
}

func (t WebFetchTool) Name() string { return "web_fetch" }

func (t WebFetchTool) Description() string {
	return "Fetch a text URL over HTTP or HTTPS and return bounded response content."
}

func (t WebFetchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The HTTP or HTTPS URL to fetch.",
			},
		},
		"required": []string{"url"},
	}
}

func (t WebFetchTool) Execute(ctx context.Context, args map[string]any) *tooling.Result {
	rawURL, _ := args["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return tooling.Error("web_fetch requires a non-empty url")
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return tooling.Error(fmt.Sprintf("web_fetch could not parse url %q: %v", rawURL, err))
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return tooling.Error(fmt.Sprintf("web_fetch only supports http and https URLs, got %q", parsedURL.Scheme))
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	maxBytes := t.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 128 * 1024
	}
	userAgent := strings.TrimSpace(t.UserAgent)
	if userAgent == "" {
		userAgent = "agent-web-fetch/1.0"
	}

	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return tooling.Error(fmt.Sprintf("web_fetch could not build request for %q: %v", rawURL, err))
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/plain, text/html, application/json, application/xml;q=0.9, */*;q=0.1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return tooling.Error(fmt.Sprintf("web_fetch request failed for %q: %v", rawURL, err))
	}
	defer resp.Body.Close()

	readLimit := maxBytes + 1
	body, err := io.ReadAll(io.LimitReader(resp.Body, readLimit))
	if err != nil {
		return tooling.Error(fmt.Sprintf("web_fetch could not read response body for %q: %v", rawURL, err))
	}

	truncated := int64(len(body)) > maxBytes
	if truncated {
		body = body[:maxBytes]
	}
	if !utf8.Valid(body) {
		return tooling.Error(fmt.Sprintf("web_fetch received non-UTF-8 content from %q", rawURL))
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	modelText := fmt.Sprintf("url: %s\nstatus: %d\ncontent_type: %s", parsedURL.String(), resp.StatusCode, contentType)
	if truncated {
		modelText += fmt.Sprintf("\ntruncated: true (max %d bytes)", maxBytes)
	}
	modelText += "\ncontent:\n" + string(body)

	return &tooling.Result{
		ForModel: modelText,
		ForUser:  fmt.Sprintf("Fetched %s", parsedURL.String()),
		Metadata: map[string]any{
			"url":          parsedURL.String(),
			"status_code":  resp.StatusCode,
			"content_type": contentType,
			"body_bytes":   len(body),
			"truncated":    truncated,
		},
	}
}
