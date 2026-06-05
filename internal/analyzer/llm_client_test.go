package analyzer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicMessagesEndpoint(t *testing.T) {
	cases := []struct {
		name string
		base string
		want string
	}{
		{
			name: "official base",
			base: "https://api.anthropic.com",
			want: "https://api.anthropic.com/v1/messages",
		},
		{
			name: "versioned proxy base",
			base: "https://example.com/v1",
			want: "https://example.com/v1/messages",
		},
		{
			name: "versioned proxy base with trailing slash",
			base: "https://example.com/v1/",
			want: "https://example.com/v1/messages",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := anthropicMessagesEndpoint(c.base); got != c.want {
				t.Fatalf("anthropicMessagesEndpoint(%q) = %q, want %q", c.base, got, c.want)
			}
		})
	}
}

func TestBuildLLMClientRequiresExplicitProvider(t *testing.T) {
	if got := BuildLLMClient("", "test-key", "", ""); got != nil {
		t.Fatalf("BuildLLMClient with empty provider returned %T, want nil", got)
	}
	if got := BuildLLMClient("offline", "test-key", "", ""); got != nil {
		t.Fatalf("BuildLLMClient with offline provider returned %T, want nil", got)
	}
}

func TestAnthropicCompleteUsesVersionedProxyBase(t *testing.T) {
	var gotPath, gotAuth, gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"{\\\"ok\\\":true}\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"))
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key", srv.URL+"/v1", "claude-sonnet-4-20250514")
	out, err := client.Complete(context.Background(), "system", "user")
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("request path = %q, want /v1/messages", gotPath)
	}
	if gotAPIKey != "test-key" {
		t.Fatalf("x-api-key = %q, want test-key", gotAPIKey)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer token", gotAuth)
	}
	if out != `{"ok":true}` {
		t.Fatalf("output = %q, want JSON payload", out)
	}
}

func TestAnthropicCompleteParsesNonStreamingJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"ok\":true}"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key", srv.URL, "claude-sonnet-4-20250514")
	out, err := client.Complete(context.Background(), "system", "user")
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if out != `{"ok":true}` {
		t.Fatalf("output = %q, want JSON payload", out)
	}
}

func TestAnthropicCompleteReturnsTruncatedOutputForRepair(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"{\\\"overall_score\\\":72\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\n"))
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key", srv.URL, "claude-sonnet-4-20250514")
	out, err := client.Complete(context.Background(), "system", "user")
	if err != nil {
		t.Fatalf("Complete returned error for repairable truncation: %v", err)
	}
	if out != `{"overall_score":72` {
		t.Fatalf("output = %q, want partial JSON", out)
	}
}

func TestAnthropicCompleteReturnsTruncatedJSONOutputForRepair(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"overall_score\":72"}],"stop_reason":"max_tokens"}`))
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key", srv.URL, "claude-sonnet-4-20250514")
	out, err := client.Complete(context.Background(), "system", "user")
	if err != nil {
		t.Fatalf("Complete returned error for repairable JSON truncation: %v", err)
	}
	if out != `{"overall_score":72` {
		t.Fatalf("output = %q, want partial JSON", out)
	}
}
