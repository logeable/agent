package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ModelErrorKind string

const (
	ModelErrorRetryable       ModelErrorKind = "retryable"
	ModelErrorContextOverflow ModelErrorKind = "context_overflow"
	ModelErrorPermanent       ModelErrorKind = "permanent"
)

// ModelError classifies provider failures into the small set of recovery
// actions the loop cares about.
type ModelError struct {
	Kind       ModelErrorKind
	Message    string
	StatusCode int
	RetryAfter time.Duration
	Err        error
}

func (e *ModelError) Error() string {
	if e == nil {
		return "model error"
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "model error"
}

func (e *ModelError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func AsModelError(err error) (*ModelError, bool) {
	if err == nil {
		return nil, false
	}
	var target *ModelError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

func classifyTransportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &ModelError{
			Kind:    ModelErrorPermanent,
			Message: fmt.Sprintf("send request: %v", err),
			Err:     err,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ModelError{
			Kind:    ModelErrorRetryable,
			Message: fmt.Sprintf("send request: %v", err),
			Err:     err,
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		kind := ModelErrorPermanent
		if netErr.Timeout() {
			kind = ModelErrorRetryable
		}
		return &ModelError{
			Kind:    kind,
			Message: fmt.Sprintf("send request: %v", err),
			Err:     err,
		}
	}
	return &ModelError{
		Kind:    ModelErrorRetryable,
		Message: fmt.Sprintf("send request: %v", err),
		Err:     err,
	}
}

func classifyHTTPError(prefix string, statusCode int, body string, headers http.Header) error {
	body = strings.TrimSpace(body)
	kind := ModelErrorPermanent
	switch {
	case isContextOverflow(statusCode, body):
		kind = ModelErrorContextOverflow
	case isRetryableHTTPStatus(statusCode):
		kind = ModelErrorRetryable
	}

	message := fmt.Sprintf("%s (%d)", prefix, statusCode)
	if body != "" {
		message += ": " + body
	}

	return &ModelError{
		Kind:       kind,
		Message:    message,
		StatusCode: statusCode,
		RetryAfter: parseRetryAfter(headers),
	}
}

func isRetryableHTTPStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isContextOverflow(statusCode int, body string) bool {
	if statusCode == http.StatusRequestEntityTooLarge {
		return true
	}
	body = strings.ToLower(strings.TrimSpace(body))
	if body == "" {
		return false
	}
	patterns := []string{
		"context length",
		"context window",
		"context too long",
		"maximum context length",
		"prompt is too long",
		"too many tokens",
		"token limit",
		"context overflow",
	}
	for _, pattern := range patterns {
		if strings.Contains(body, pattern) {
			return true
		}
	}
	return false
}

func parseRetryAfter(headers http.Header) time.Duration {
	if headers == nil {
		return 0
	}
	value := strings.TrimSpace(headers.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		wait := time.Until(when)
		if wait > 0 {
			return wait
		}
	}
	return 0
}
