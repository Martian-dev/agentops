package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Martian-dev/agentops/internal/llm/tracectx"
)

const (
	defaultOpenRouterURL = "https://openrouter.ai/api/v1/chat/completions"
	defaultGeminiURL     = "https://generativelanguage.googleapis.com/v1beta/models"
	defaultHTTPTimeout   = 20 * time.Second

	defaultOpenRouterModel = "google/gemini-2.0-flash-001"
	defaultGeminiModel     = "gemini-2.0-flash"
)

// LLMClient is a provider-agnostic interface for text completion.
type LLMClient interface {
	Complete(ctx context.Context, systemPrompt, userMessage string, temp float32) (string, int, int, error)
}

// HTTPStatusError carries upstream status and response body.
type HTTPStatusError struct {
	Provider   string
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("%s request failed with status %d: %s", e.Provider, e.StatusCode, strings.TrimSpace(e.Body))
}

// IsServerError returns true only for 500/502/503/504 status failures.
func IsServerError(err error) bool {
	var statusErr *HTTPStatusError
	if !AsHTTPStatusError(err, &statusErr) {
		return false
	}
	switch statusErr.StatusCode {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// AsHTTPStatusError unwraps err into *HTTPStatusError.
func AsHTTPStatusError(err error, target **HTTPStatusError) bool {
	if err == nil || target == nil {
		return false
	}
	statusErr, ok := err.(*HTTPStatusError)
	if ok {
		*target = statusErr
		return true
	}
	wrapped := unwrap(err)
	for wrapped != nil {
		statusErr, ok = wrapped.(*HTTPStatusError)
		if ok {
			*target = statusErr
			return true
		}
		wrapped = unwrap(wrapped)
	}
	return false
}

type wrapper interface{ Unwrap() error }

func unwrap(err error) error {
	w, ok := err.(wrapper)
	if !ok {
		return nil
	}
	return w.Unwrap()
}

// OpenRouterClient is the primary provider client.
type OpenRouterClient struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

func NewOpenRouterClientFromEnv() *OpenRouterClient {
	return &OpenRouterClient{
		APIKey:  strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")),
		Model:   strings.TrimSpace(os.Getenv("OPENROUTER_MODEL")),
		BaseURL: strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")),
		HTTPClient: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
	}
}

func (c *OpenRouterClient) Complete(ctx context.Context, systemPrompt, userMessage string, temp float32) (string, int, int, error) {
	if c == nil {
		return "", 0, 0, fmt.Errorf("openrouter client is nil")
	}
	if c.APIKey == "" {
		return "", 0, 0, fmt.Errorf("OPENROUTER_API_KEY is required")
	}
	model := c.Model
	if model == "" {
		model = defaultOpenRouterModel
	}
	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = defaultOpenRouterURL
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}

	body, err := json.Marshal(map[string]interface{}{
		"model":       model,
		"temperature": temp,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMessage},
		},
	})
	if err != nil {
		return "", 0, 0, fmt.Errorf("marshal openrouter request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return "", 0, 0, fmt.Errorf("build openrouter request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, 0, fmt.Errorf("openrouter request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, 0, fmt.Errorf("read openrouter response: %w", err)
	}

	if resp.StatusCode >= 300 {
		return "", 0, 0, &HTTPStatusError{Provider: "openrouter", StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var parsed struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", 0, 0, fmt.Errorf("parse openrouter response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", 0, 0, fmt.Errorf("openrouter returned no choices")
	}

	return parsed.Choices[0].Message.Content, parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens, nil
}

// GeminiClient is the direct Gemini fallback client.
type GeminiClient struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

func NewGeminiClientFromEnv() *GeminiClient {
	return &GeminiClient{
		APIKey:  strings.TrimSpace(os.Getenv("GEMINI_API_KEY")),
		Model:   strings.TrimSpace(os.Getenv("GEMINI_MODEL")),
		BaseURL: strings.TrimSpace(os.Getenv("GEMINI_BASE_URL")),
		HTTPClient: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
	}
}

func (c *GeminiClient) Complete(ctx context.Context, systemPrompt, userMessage string, temp float32) (string, int, int, error) {
	if c == nil {
		return "", 0, 0, fmt.Errorf("gemini client is nil")
	}
	if c.APIKey == "" {
		return "", 0, 0, fmt.Errorf("GEMINI_API_KEY is required")
	}
	model := c.Model
	if model == "" {
		model = defaultGeminiModel
	}
	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = defaultGeminiURL
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}

	body, err := json.Marshal(map[string]interface{}{
		"system_instruction": map[string]interface{}{
			"parts": []map[string]string{{"text": systemPrompt}},
		},
		"contents": []map[string]interface{}{{
			"role":  "user",
			"parts": []map[string]string{{"text": userMessage}},
		}},
		"generationConfig": map[string]interface{}{
			"temperature": temp,
		},
	})
	if err != nil {
		return "", 0, 0, fmt.Errorf("marshal gemini request: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + "/" + model + ":generateContent?key=" + c.APIKey
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", 0, 0, fmt.Errorf("build gemini request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, 0, fmt.Errorf("gemini request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, 0, fmt.Errorf("read gemini response: %w", err)
	}

	if resp.StatusCode >= 300 {
		return "", 0, 0, &HTTPStatusError{Provider: "gemini", StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", 0, 0, fmt.Errorf("parse gemini response: %w", err)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", 0, 0, fmt.Errorf("gemini returned empty candidates")
	}

	return parsed.Candidates[0].Content.Parts[0].Text, parsed.UsageMetadata.PromptTokenCount, parsed.UsageMetadata.CandidatesTokenCount, nil
}

// FallbackClient retries OpenRouter once on 5xx, then switches to Gemini.
type FallbackClient struct {
	Primary   LLMClient
	Secondary LLMClient
}

func NewFallbackClientFromEnv() *FallbackClient {
	return &FallbackClient{
		Primary:   NewOpenRouterClientFromEnv(),
		Secondary: NewGeminiClientFromEnv(),
	}
}

func (f *FallbackClient) Complete(ctx context.Context, systemPrompt, userMessage string, temp float32) (string, int, int, error) {
	if f == nil {
		return "", 0, 0, fmt.Errorf("fallback client is nil")
	}
	if f.Primary == nil {
		return "", 0, 0, fmt.Errorf("primary llm client is required")
	}

	resp, in, out, err := f.Primary.Complete(ctx, systemPrompt, userMessage, temp)
	if err == nil || !IsServerError(err) {
		return resp, in, out, err
	}

	// Spec: OpenRouter 5xx -> retry once before provider fallback.
	resp, in, out, retryErr := f.Primary.Complete(ctx, systemPrompt, userMessage, temp)
	if retryErr == nil || !IsServerError(retryErr) {
		return resp, in, out, retryErr
	}

	if f.Secondary == nil {
		return "", 0, 0, retryErr
	}

	tracectx.EmitProviderFallback(ctx, retryErr)
	return f.Secondary.Complete(ctx, systemPrompt, userMessage, temp)
}
