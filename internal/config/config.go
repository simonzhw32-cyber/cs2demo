package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	HTTPAddr    string
	DataDir     string
	SQLitePath  string
	LLMProvider string
	LLMAPIKey   string
	LLMBaseURL  string
	LLMModel    string
	WorkerCount int
	MaxUploadMB int
}

func Load() Config {
	provider := normalizeProvider(env("LLM_PROVIDER", "offline"))
	apiKey := ""
	baseURL := ""
	if provider != "offline" {
		apiKey = env("LLM_API_KEY", "")
		baseURL = env("LLM_BASE_URL", "")
		if apiKey == "" {
			switch provider {
			case "anthropic":
				apiKey = env("ANTHROPIC_AUTH_TOKEN", env("ANTHROPIC_API_KEY", ""))
				if baseURL == "" {
					baseURL = env("ANTHROPIC_BASE_URL", "")
				}
			case "openai":
				apiKey = env("OPENAI_API_KEY", "")
				if baseURL == "" {
					baseURL = env("OPENAI_BASE_URL", "")
				}
			}
		}
	}
	return Config{
		HTTPAddr:    env("HTTP_ADDR", ":8080"),
		DataDir:     env("DATA_DIR", "./data"),
		SQLitePath:  env("SQLITE_PATH", "./data/cs2demo.db"),
		LLMProvider: provider,
		LLMAPIKey:   apiKey,
		LLMBaseURL:  baseURL,
		LLMModel:    env("LLM_MODEL", defaultModel(provider)),
		WorkerCount: envInt("WORKER_COUNT", 2),
		MaxUploadMB: envInt("MAX_UPLOAD_MB", 1024),
	}
}

func normalizeProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "offline", "off", "none", "local":
		return "offline"
	case "openai":
		return "openai"
	case "anthropic":
		return "anthropic"
	default:
		return "offline"
	}
}

func defaultModel(provider string) string {
	if provider == "openai" {
		return "gpt-4o-mini"
	}
	if provider == "offline" {
		return ""
	}
	return "claude-sonnet-4-20250514"
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
