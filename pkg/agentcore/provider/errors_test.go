package provider

import (
	"net/http"
	"testing"
	"time"
)

func TestClassifyHTTPErrorContextOverflow(t *testing.T) {
	err := classifyHTTPError("provider error", http.StatusBadRequest, "maximum context length exceeded", nil)

	modelErr, ok := AsModelError(err)
	if !ok {
		t.Fatalf("error = %T, want *ModelError", err)
	}
	if modelErr.Kind != ModelErrorContextOverflow {
		t.Fatalf("kind = %q, want %q", modelErr.Kind, ModelErrorContextOverflow)
	}
}

func TestClassifyHTTPErrorRetryable(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Retry-After", "3")

	err := classifyHTTPError("provider error", http.StatusTooManyRequests, "slow down", headers)

	modelErr, ok := AsModelError(err)
	if !ok {
		t.Fatalf("error = %T, want *ModelError", err)
	}
	if modelErr.Kind != ModelErrorRetryable {
		t.Fatalf("kind = %q, want %q", modelErr.Kind, ModelErrorRetryable)
	}
	if modelErr.RetryAfter != 3*time.Second {
		t.Fatalf("retry after = %v, want 3s", modelErr.RetryAfter)
	}
}
