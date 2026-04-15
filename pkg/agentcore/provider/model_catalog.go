package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// ModelInfo describes one discoverable provider model.
type ModelInfo struct {
	ID      string
	OwnedBy string
}

// ModelCatalogConfig describes how to query a provider's model listing API.
type ModelCatalogConfig struct {
	Kind       string
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// ListModels fetches the provider's available models using the provider's
// native listing endpoint.
func ListModels(ctx context.Context, cfg ModelCatalogConfig) ([]ModelInfo, error) {
	kind := strings.ToLower(strings.TrimSpace(cfg.Kind))
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if kind == "" {
		return nil, fmt.Errorf("provider kind is required")
	}
	if baseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultOpenAICompatTimeout}
	}

	switch kind {
	case "openai", "openai_response":
		return listOpenAIModels(ctx, client, baseURL+"/models", strings.TrimSpace(cfg.APIKey))
	case "ollama":
		return listOllamaModels(ctx, client, baseURL+"/tags", strings.TrimSpace(cfg.APIKey))
	default:
		return nil, fmt.Errorf("unsupported provider kind %q", cfg.Kind)
	}
}

func listOpenAIModels(ctx context.Context, client *http.Client, endpoint, apiKey string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, classifyHTTPError("provider error", resp.StatusCode, string(body), resp.Header)
	}

	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	models := make([]ModelInfo, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		models = append(models, ModelInfo{
			ID:      id,
			OwnedBy: strings.TrimSpace(item.OwnedBy),
		})
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
	return models, nil
}

func listOllamaModels(ctx context.Context, client *http.Client, endpoint, apiKey string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, classifyHTTPError("provider error", resp.StatusCode, string(body), resp.Header)
	}

	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	models := make([]ModelInfo, 0, len(payload.Models))
	for _, item := range payload.Models {
		id := strings.TrimSpace(item.Name)
		if id == "" {
			continue
		}
		models = append(models, ModelInfo{ID: id})
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
	return models, nil
}
