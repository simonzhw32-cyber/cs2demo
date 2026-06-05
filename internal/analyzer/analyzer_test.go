package analyzer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cs2demo/platform/internal/domain"
	"github.com/cs2demo/platform/internal/prokb"
)

type fakeLLMClient struct {
	out string
	err error
}

func (f fakeLLMClient) Complete(context.Context, string, string) (string, error) {
	return f.out, f.err
}

func (f fakeLLMClient) Name() string { return "fake" }

func TestAnalyzeUsesSuccessfulLLMReport(t *testing.T) {
	a := &Analyzer{
		llm: fakeLLMClient{out: `{
			"overall_score": 77,
			"verdict": "LLM generated report",
			"strengths": [],
			"weaknesses": [],
			"suggestions": [],
			"round_analyses": [],
			"pro_reference": "test"
		}`},
		kb: prokb.New(),
	}
	stats := domain.MatchStats{
		Map: "de_inferno",
		Target: domain.PlayerStats{
			Name:   "target",
			Team:   "T",
			Kills:  10,
			Deaths: 8,
			ADR:    80,
			KAST:   70,
		},
	}

	report, err := a.Analyze(context.Background(), "demo-id", stats)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if strings.Contains(report.Verdict, "LLM 调用失败") {
		t.Fatalf("successful LLM report was marked as call failure: %q", report.Verdict)
	}
	if report.AnalysisSource != "llm" {
		t.Fatalf("AnalysisSource = %q, want llm", report.AnalysisSource)
	}
	if !strings.Contains(report.Verdict, "LLM generated report") {
		t.Fatalf("verdict did not preserve LLM output: %q", report.Verdict)
	}
}

func TestAnalyzeFallbackIncludesWarning(t *testing.T) {
	a := &Analyzer{
		llm: fakeLLMClient{err: errors.New("anthropic 404: model not found")},
		kb:  prokb.New(),
	}
	stats := domain.MatchStats{
		Map: "de_inferno",
		Target: domain.PlayerStats{
			Name:   "target",
			Team:   "T",
			Kills:  10,
			Deaths: 8,
			ADR:    80,
			KAST:   70,
		},
	}

	report, err := a.Analyze(context.Background(), "demo-id", stats)
	if err == nil {
		t.Fatal("expected Analyze to return warning error")
	}
	if report.AnalysisSource != "offline_fallback" {
		t.Fatalf("AnalysisSource = %q, want offline_fallback", report.AnalysisSource)
	}
	if !strings.Contains(report.AnalysisWarning, "anthropic 404") {
		t.Fatalf("AnalysisWarning = %q, want LLM error detail", report.AnalysisWarning)
	}
	if !strings.Contains(report.Verdict, "LLM 调用失败") {
		t.Fatalf("fallback verdict did not identify call failure: %q", report.Verdict)
	}
}

func TestOfflineReportUsesChineseRoleInVerdict(t *testing.T) {
	kb := prokb.New()
	stats := domain.MatchStats{
		Map: "de_overpass",
		Target: domain.PlayerStats{
			Name:        "JamYoung",
			Team:        "T",
			Kills:       9,
			Deaths:      15,
			Assists:     3,
			ADR:         74.3,
			KAST:        42.1,
			WeaponKills: map[string]int{},
		},
	}
	role := kb.RoleHints(stats.Target)
	baseline := kb.Lookup(stats.Map, role)
	report := offlineReport("demo-id", stats, baseline, buildComparison(stats.Target, baseline))

	if strings.Contains(report.Verdict, "rifler") {
		t.Fatalf("verdict leaked English role: %q", report.Verdict)
	}
	if strings.Contains(report.Verdict, "整体 ") {
		t.Fatalf("verdict contains awkward spacing: %q", report.Verdict)
	}
	if !strings.Contains(report.Verdict, "职业步枪手基线") {
		t.Fatalf("verdict did not use Chinese role display: %q", report.Verdict)
	}
}
