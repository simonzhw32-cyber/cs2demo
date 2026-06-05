package config

import "testing"

func TestLoadDefaultsToOffline(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	cfg := Load()
	if cfg.LLMProvider != "offline" {
		t.Fatalf("LLMProvider = %q, want offline", cfg.LLMProvider)
	}
	if cfg.LLMAPIKey != "" {
		t.Fatalf("LLMAPIKey = %q, want empty", cfg.LLMAPIKey)
	}
	if cfg.LLMModel != "" {
		t.Fatalf("LLMModel = %q, want empty", cfg.LLMModel)
	}
}

func TestLoadOfflineIgnoresProviderSpecificKeys(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "")
	t.Setenv("LLM_API_KEY", "generic-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "anthropic-key")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-api-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")

	cfg := Load()
	if cfg.LLMProvider != "offline" {
		t.Fatalf("LLMProvider = %q, want offline", cfg.LLMProvider)
	}
	if cfg.LLMAPIKey != "" {
		t.Fatalf("LLMAPIKey = %q, want empty in offline mode", cfg.LLMAPIKey)
	}
}

func TestLoadNormalizesProviderBeforeKeyLookup(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "OpenAI")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "anthropic-key")

	cfg := Load()
	if cfg.LLMProvider != "openai" {
		t.Fatalf("LLMProvider = %q, want openai", cfg.LLMProvider)
	}
	if cfg.LLMAPIKey != "openai-key" {
		t.Fatalf("LLMAPIKey did not use OPENAI_API_KEY")
	}
	if cfg.LLMModel != "gpt-4o-mini" {
		t.Fatalf("LLMModel = %q, want gpt-4o-mini", cfg.LLMModel)
	}
}
