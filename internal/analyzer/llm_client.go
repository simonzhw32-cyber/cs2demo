package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type LLMClient interface {
	Complete(ctx context.Context, system, user string) (string, error)
	Name() string
}

const llmMaxRetries = 3

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "unexpected EOF"),
		strings.Contains(s, "EOF"),
		strings.Contains(s, "connection reset"),
		strings.Contains(s, "broken pipe"),
		strings.Contains(s, "i/o timeout"),
		strings.Contains(s, "context deadline exceeded"),
		strings.Contains(s, " 502"),
		strings.Contains(s, " 503"),
		strings.Contains(s, " 504"),
		strings.Contains(s, " 529"):
		return true
	}
	return false
}

func backoff(attempt int) time.Duration {
	base := time.Duration(1<<attempt) * time.Second
	if base > 8*time.Second {
		base = 8 * time.Second
	}
	return base
}

type AnthropicClient struct {
	apiKey  string
	baseURL string
	model   string
	hc      *http.Client
}

func NewAnthropicClient(apiKey, baseURL, model string) *AnthropicClient {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}
	return &AnthropicClient{
		apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/"), model: model,
		hc: &http.Client{Timeout: 300 * time.Second},
	}
}

func (a *AnthropicClient) Name() string { return "anthropic:" + a.model }

func (a *AnthropicClient) Complete(ctx context.Context, system, user string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < llmMaxRetries; attempt++ {
		out, err := a.callOnce(ctx, system, user)
		if err == nil {
			if attempt > 0 {
				log.Printf("[llm anthropic] succeeded on attempt %d", attempt+1)
			}
			return out, nil
		}
		lastErr = err
		var truncated *TruncatedError
		if errors.As(err, &truncated) && out != "" {
			log.Printf("[llm anthropic] output truncated, attempting to repair %d bytes", len(out))
			return out, nil
		}
		if !isRetryable(err) {
			log.Printf("[llm anthropic] non-retryable err: %v", err)
			return "", err
		}
		wait := backoff(attempt)
		log.Printf("[llm anthropic] attempt %d/%d failed (%v), retrying in %s", attempt+1, llmMaxRetries, err, wait)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}
	return "", fmt.Errorf("after %d retries: %w", llmMaxRetries, lastErr)
}

func (a *AnthropicClient) callOnce(ctx context.Context, system, user string) (string, error) {
	body := map[string]any{
		"model":      a.model,
		"max_tokens": 6000,
		"stream":     true,
		"system":     system,
		"messages": []map[string]any{
			{"role": "user", "content": user},
		},
	}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", anthropicMessagesEndpoint(a.baseURL), bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("anthropic %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return parseAnthropicResponse(raw)
}

func parseAnthropicResponse(raw []byte) (string, error) {
	if bytes.Contains(raw, []byte("data:")) {
		return parseAnthropicStream(string(raw))
	}
	return parseAnthropicJSON(raw)
}

func parseAnthropicStream(raw string) (string, error) {
	var sb strings.Builder
	stopReason := ""
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" {
				sb.WriteString(ev.Delta.Text)
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				stopReason = ev.Delta.StopReason
			}
		}
	}
	out := sb.String()
	if stopReason == "max_tokens" {
		return out, &TruncatedError{Got: len(out), StopReason: stopReason}
	}
	if out == "" {
		return "", fmt.Errorf("empty stream output")
	}
	return out, nil
}

func parseAnthropicJSON(raw []byte) (string, error) {
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("decode anthropic json: %w", err)
	}
	var sb strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	out := sb.String()
	if parsed.StopReason == "max_tokens" {
		return out, &TruncatedError{Got: len(out), StopReason: parsed.StopReason}
	}
	if out == "" {
		return "", fmt.Errorf("empty json output")
	}
	return out, nil
}

func anthropicMessagesEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/messages"
	}
	return baseURL + "/v1/messages"
}

type TruncatedError struct {
	Got        int
	StopReason string
}

func (e *TruncatedError) Error() string {
	return fmt.Sprintf("llm output truncated (stop_reason=%s, got=%d bytes)", e.StopReason, e.Got)
}

type OpenAIClient struct {
	apiKey  string
	baseURL string
	model   string
	hc      *http.Client
}

func NewOpenAIClient(apiKey, baseURL, model string) *OpenAIClient {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	return &OpenAIClient{
		apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/"), model: model,
		hc: &http.Client{Timeout: 300 * time.Second},
	}
}

func (o *OpenAIClient) Name() string { return "openai:" + o.model }

func (o *OpenAIClient) Complete(ctx context.Context, system, user string) (string, error) {
	body := map[string]any{
		"model":           o.model,
		"temperature":     0.4,
		"response_format": map[string]string{"type": "json_object"},
		"messages": []map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("empty choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

func BuildLLMClient(provider, apiKey, baseURL, model string) LLMClient {
	if apiKey == "" {
		return nil
	}
	switch strings.ToLower(provider) {
	case "openai":
		return NewOpenAIClient(apiKey, baseURL, model)
	case "anthropic":
		return NewAnthropicClient(apiKey, baseURL, model)
	}
	return nil
}
