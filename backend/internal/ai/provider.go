package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ProviderConfig struct {
	BaseURL, APIKey, ChatModel, EmbeddingModel string
	Timeout                                    time.Duration
	MaxResponseBytes                           int64
}
type DisabledProvider struct{}

func (DisabledProvider) Enabled() bool { return false }
func (DisabledProvider) Chat([]Message) (string, int, int, error) {
	return "", 0, 0, fmt.Errorf("LLM provider is not configured")
}
func (DisabledProvider) Embed(string) ([]float64, error) {
	return nil, fmt.Errorf("embedding provider is not configured")
}
func (DisabledProvider) EmbeddingsEnabled() bool { return false }

type OpenAIProvider struct {
	cfg    ProviderConfig
	client *http.Client
}

func NewOpenAIProvider(cfg ProviderConfig, client *http.Client) Provider {
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.ChatModel) == "" {
		return DisabledProvider{}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = 2 << 20
	}
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &OpenAIProvider{cfg: cfg, client: client}
}
func (p *OpenAIProvider) Enabled() bool           { return true }
func (p *OpenAIProvider) EmbeddingsEnabled() bool { return p.cfg.EmbeddingModel != "" }
func (p *OpenAIProvider) Chat(messages []Message) (string, int, int, error) {
	body := map[string]any{"model": p.cfg.ChatModel, "messages": messages, "temperature": 0.2}
	data, err := json.Marshal(body)
	if err != nil {
		return "", 0, 0, fmt.Errorf("marshal chat request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, p.cfg.BaseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return "", 0, 0, err
	}
	p.auth(req)
	res, err := p.client.Do(req)
	if err != nil {
		return "", 0, 0, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, p.cfg.MaxResponseBytes))
	if err != nil {
		return "", 0, 0, err
	}
	if res.StatusCode >= 300 {
		return "", 0, 0, fmt.Errorf("chat completion failed: %s", res.Status)
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", 0, 0, err
	}
	if len(out.Choices) == 0 {
		return "", 0, 0, fmt.Errorf("chat completion returned no choices")
	}
	return out.Choices[0].Message.Content, out.Usage.PromptTokens, out.Usage.CompletionTokens, nil
}
func (p *OpenAIProvider) Embed(text string) ([]float64, error) {
	if !p.EmbeddingsEnabled() {
		return nil, fmt.Errorf("embedding model is not configured")
	}
	body := map[string]any{"model": p.cfg.EmbeddingModel, "input": text}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, p.cfg.BaseURL+"/embeddings", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	p.auth(req)
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, p.cfg.MaxResponseBytes))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding failed: %s", res.Status)
	}
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("embedding returned no vectors")
	}
	return out.Data[0].Embedding, nil
}
func (p *OpenAIProvider) auth(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
}
