package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListModelsOpenAISortsAndPreservesOwner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"z-model","owned_by":"org-z"},{"id":"a-model","owned_by":"org-a"}]}`))
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), ModelCatalogConfig{
		Kind:    "openai",
		BaseURL: server.URL,
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].ID != "a-model" || models[0].OwnedBy != "org-a" {
		t.Fatalf("models[0] = %+v", models[0])
	}
	if models[1].ID != "z-model" || models[1].OwnedBy != "org-z" {
		t.Fatalf("models[1] = %+v", models[1])
	}
}

func TestListModelsOllamaUsesTagsEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tags" {
			t.Fatalf("path = %q, want /tags", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen2.5:14b"},{"name":"llama3.2:3b"}]}`))
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), ModelCatalogConfig{
		Kind:    "ollama",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].ID != "llama3.2:3b" {
		t.Fatalf("models[0].ID = %q, want llama3.2:3b", models[0].ID)
	}
	if models[1].ID != "qwen2.5:14b" {
		t.Fatalf("models[1].ID = %q, want qwen2.5:14b", models[1].ID)
	}
}
