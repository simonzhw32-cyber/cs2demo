package analyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cs2demo/platform/internal/callouts"
	"github.com/cs2demo/platform/internal/domain"
	"github.com/cs2demo/platform/internal/prokb"
)

type Analyzer struct {
	llm LLMClient
	kb  prokb.KB
}

func New(provider, apiKey, baseURL, model string, kb prokb.KB) *Analyzer {
	return &Analyzer{llm: BuildLLMClient(provider, apiKey, baseURL, model), kb: kb}
}

func (a *Analyzer) ProviderName() string {
	if a.llm == nil {
		return "offline"
	}
	return a.llm.Name()
}

func DumpPrompt(stats domain.MatchStats, kb prokb.KB) (system, user string) {
	role := kb.RoleHints(stats.Target)
	baseline := kb.Lookup(stats.Map, role)
	cmp := buildComparison(stats.Target, baseline)
	return systemPrompt, buildPrompt(stats, baseline, cmp)
}

func (a *Analyzer) Analyze(ctx context.Context, demoID string, stats domain.MatchStats) (domain.AnalysisReport, error) {
	role := a.kb.RoleHints(stats.Target)
	baseline := a.kb.Lookup(stats.Map, role)
	comparison := buildComparison(stats.Target, baseline)

	if a.llm == nil {
		report := offlineReport(demoID, stats, baseline, comparison)
		report.AnalysisSource = "offline"
		fillSummaryEvals(&report, stats)
		normalizeTerms(&report, stats.Map)
		return report, nil
	}

	prompt := buildPrompt(stats, baseline, comparison)
	raw, err := a.llm.Complete(ctx, systemPrompt, prompt)
	if err != nil {
		report := offlineReport(demoID, stats, baseline, comparison)
		report.Verdict = "[LLM 调用失败，回退离线规则] " + report.Verdict
		report.AnalysisSource = "offline_fallback"
		report.AnalysisWarning = "llm call: " + safeAnalysisWarning(err)
		fillSummaryEvals(&report, stats)
		normalizeTerms(&report, stats.Map)
		return report, fmt.Errorf("llm call: %w", err)
	}

	report, err := parseLLMReport(raw)
	if err != nil {
		report := offlineReport(demoID, stats, baseline, comparison)
		report.Verdict = "[LLM 输出无法解析，回退离线规则] " + report.Verdict
		report.AnalysisSource = "offline_fallback"
		report.AnalysisWarning = "parse llm json: " + safeAnalysisWarning(err)
		fillSummaryEvals(&report, stats)
		normalizeTerms(&report, stats.Map)
		return report, fmt.Errorf("parse llm json: %w (raw_head=%s ... raw_tail=%s)", err, truncate(raw, 200), tail(raw, 200))
	}

	report.DemoID = demoID
	report.GeneratedAt = time.Now().UTC()
	report.AnalysisSource = "llm"
	report.Comparison = comparison
	if report.ProReference == "" {
		report.ProReference = baseline.Notes
	}
	mergeOfflineForMissing(&report, stats, baseline)
	fillSummaryEvals(&report, stats)
	normalizeTerms(&report, stats.Map)
	return report, nil
}

func safeAnalysisWarning(err error) string {
	if err == nil {
		return ""
	}
	return truncate(strings.ReplaceAll(err.Error(), "\n", " "), 500)
}

// fillSummaryEvals 用规则补 5 个顶层 eval 字段；若 LLM 已经写了就保留。
func fillSummaryEvals(r *domain.AnalysisReport, stats domain.MatchStats) {
	if r.AimEval == "" {
		r.AimEval = offlineAimEval(stats.Target.AimQuality)
	}
	if r.PressureEval == "" {
		r.PressureEval = offlinePressureEval(stats.Target.Pressure)
	}
	if r.TeamSyncEval == "" {
		r.TeamSyncEval = offlineTeamSyncEval(stats.TeamSync)
	}
	if r.SmokeEval == "" {
		r.SmokeEval = offlineSmokeEval(stats.SmokeReport)
	}
	if r.MovementEval == "" {
		r.MovementEval = offlineMovementEval(stats.Movement)
	}
}

func mergeOfflineForMissing(r *domain.AnalysisReport, stats domain.MatchStats, baseline domain.ProBaseline) {
	off := offlineReport(r.DemoID, stats, baseline, r.Comparison)
	patched := []string{}
	if len(r.Strengths) == 0 {
		r.Strengths = off.Strengths
		patched = append(patched, "亮点")
	}
	if len(r.Weaknesses) == 0 {
		r.Weaknesses = off.Weaknesses
		patched = append(patched, "短板")
	}
	if len(r.Suggestions) == 0 {
		r.Suggestions = off.Suggestions
		patched = append(patched, "建议")
	}
	if len(r.RoundAnalyses) == 0 {
		r.RoundAnalyses = off.RoundAnalyses
		patched = append(patched, "回合分析")
	}
	if r.Verdict == "" {
		r.Verdict = off.Verdict
	}
	if r.OverallScore == 0 {
		r.OverallScore = off.OverallScore
	}
	if len(patched) > 0 {
		r.Verdict = "[LLM 输出截断, " + strings.Join(patched, "/") + " 由规则补齐] " + r.Verdict
	}
}

var termReplacements = []struct{ from, to string }{
	{"pistol局", "手枪局"},
	{"pistol 局", "手枪局"},
	{"Pistol局", "手枪局"},
	{"PISTOL局", "手枪局"},
	{"pistol round", "手枪局"},
	{"pistol-round", "手枪局"},
	{"force-buy", "强起局"},
	{"force buy", "强起局"},
	{"forcebuy", "强起局"},
	{"force局", "强起局"},
	{"Force局", "强起局"},
	{"force 局", "强起局"},
	{"semi-buy", "半起局"},
	{"semi buy", "半起局"},
	{"semibuy", "半起局"},
	{"semi局", "半起局"},
	{"Semi局", "半起局"},
	{"full-buy", "全装局"},
	{"full buy", "全装局"},
	{"fullbuy", "全装局"},
	{"full局", "全装局"},
	{"Full局", "全装局"},
	{"eco局", "经济局"},
	{"Eco局", "经济局"},
	{"ECO局", "经济局"},
	{"eco 局", "经济局"},
	{"eco round", "经济局"},
	{"clutch局", "残局"},
	{"clutch 局", "残局"},
	{"Clutch", "残局"},
	{"CLUTCH", "残局"},
}

var wordBoundaryReplacements = []struct{ from, to string }{
	{"pistol", "手枪局"},
	{"Pistol", "手枪局"},
	{"PISTOL", "手枪局"},
	{"force", "强起局"},
	{"Force", "强起局"},
	{"FORCE", "强起局"},
	{"semi", "半起局"},
	{"Semi", "半起局"},
	{"SEMI", "半起局"},
	{"eco", "经济局"},
	{"Eco", "经济局"},
	{"ECO", "经济局"},
	{"clutch", "残局"},
}

func cleanTerms(s string) string {
	if s == "" {
		return s
	}
	for _, r := range termReplacements {
		s = strings.ReplaceAll(s, r.from, r.to)
	}
	for _, r := range wordBoundaryReplacements {
		s = replaceWordBoundary(s, r.from, r.to)
	}
	return s
}

func replaceWordBoundary(s, from, to string) string {
	if !strings.Contains(s, from) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		idx := strings.Index(s[i:], from)
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		abs := i + idx
		b.WriteString(s[i:abs])
		prevOK := abs == 0 || !isWordChar(s[abs-1])
		end := abs + len(from)
		nextOK := end >= len(s) || !isWordChar(s[end])
		if prevOK && nextOK {
			b.WriteString(to)
		} else {
			b.WriteString(from)
		}
		i = end
	}
	return b.String()
}

func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

func normalizeTerms(r *domain.AnalysisReport, mapName string) {
	clean := func(s string) string {
		return callouts.NormalizeText(mapName, cleanTerms(s))
	}
	r.Verdict = clean(r.Verdict)
	r.ProReference = clean(r.ProReference)
	for i := range r.Strengths {
		r.Strengths[i].Title = clean(r.Strengths[i].Title)
		r.Strengths[i].Detail = clean(r.Strengths[i].Detail)
	}
	for i := range r.Weaknesses {
		r.Weaknesses[i].Title = clean(r.Weaknesses[i].Title)
		r.Weaknesses[i].Detail = clean(r.Weaknesses[i].Detail)
	}
	for i := range r.Suggestions {
		r.Suggestions[i].Title = clean(r.Suggestions[i].Title)
		r.Suggestions[i].Detail = clean(r.Suggestions[i].Detail)
	}
	for i := range r.RoundAnalyses {
		r.RoundAnalyses[i].Tactic = clean(r.RoundAnalyses[i].Tactic)
		r.RoundAnalyses[i].Mistake = clean(r.RoundAnalyses[i].Mistake)
		r.RoundAnalyses[i].Clutch = clean(r.RoundAnalyses[i].Clutch)
		r.RoundAnalyses[i].GrenadeEval = clean(r.RoundAnalyses[i].GrenadeEval)
		r.RoundAnalyses[i].MapControl = clean(r.RoundAnalyses[i].MapControl)
		r.RoundAnalyses[i].UtilityAssist = clean(r.RoundAnalyses[i].UtilityAssist)
		r.RoundAnalyses[i].OpponentPredict = clean(r.RoundAnalyses[i].OpponentPredict)
		r.RoundAnalyses[i].Adjustment = clean(r.RoundAnalyses[i].Adjustment)
	}
}

const systemPrompt = `你是一名资深的 CS2 教练，给中高级玩家做录像复盘。
输出必须是严格的 JSON 对象，符合下面 schema，不要多余文字、不要 markdown 包裹：
{
  "overall_score": <0-100 的整数>,
  "verdict":      "<一句话总结这场表现>",
  "strengths":    [{"title":"...","detail":"...","round":<可选整数>}],
  "weaknesses":   [{"title":"...","detail":"...","round":<可选整数>}],
  "suggestions":  [{"title":"...","detail":"..."}],
  "aim_eval":         "<枪法画像评注：基于'枪法画像'段，引用具体距离桶/HS%/最快连杀/首杀对枪胜负，指出强项和短板>",
  "pressure_eval":    "<高压表现评注：基于'压力下表现'段，对比高压 KAST vs 普通 KAST，点评残局/经济局/赛点表现，连败心态调整建议>",
  "team_sync_eval":   "<团队配合评注：基于'团队配合'段，引用 trade 率/平均 trade 间隔/孤立死次数，点评跟进意识和最佳搭档>",
  "smoke_eval":       "<烟雾覆盖率评注：基于'烟雾覆盖率'段，引用准确率/漏掉的关键控点，点评 lineup 熟练度>",
  "movement_eval":    "<移动纪律评注：基于'移动纪律'段，引用跑动开枪率/早期突进死，点评急停/卡点纪律>",
  "round_analyses": [
    {
      "round": <回合号>,
      "tactic": "<战术执行评注：先指出本回合全队战术意图（例如：T 方爆 A / 假 B 真 A / 中路控制 / 双 split），再说你在这个意图里扮演了什么角色、有没有跟上队友节奏。必须引用 prompt 里出现过的具体地图位置（不要从其他地图借词）和队友动作（谁先死在哪、谁安弹、进攻方向）。如果有'阶段=X|节奏=Y'，必须引用并解读>",
      "mistake": "<具体失误：例如：队友打 A，你单走 B 道；爆点后你没跟进；CT 半场提前压点被秒>",
      "clutch": "<残局思路评注：站位、分散对手、打时间、利用炸弹时间等>",
      "grenade_eval":     "<默认道具评价：基于 grenades[] 数据，逐颗点评：道具类型/投出位置/落点/造成伤害/影响人数。区分'到位'（落点封死关键视野/伤害>20/闪到 2+ 敌人）和'浪费'（投到空气/闪到队友/位置错误）。如果 grenades 为空，写'本回合无道具使用'>",
      "map_control":      "<回合中地图控制：基于 position_track 时间序列和 zone_occupancy 占比，分析你的走位轨迹。要点：① 是否抢到本图关键控点（zone 名一律用 prompt 里给的）；② zone 切换是否随团队节奏；③ control_score 分数解读（>60 高强度控图，30-60 中等，<30 被动卡点）。引用具体时间戳和位置>",
      "utility_assist":   "<辅助道具使用：基于 flash_assists[] 和 grenades[].enemies_flashed/team_flashed。要点：① 闪盲了几个敌人/时长；② 有没有闪到队友（负面）；③ 闪光时机是否配合队友进攻（爆点前 0.5-2s 是黄金窗口）。如无辅助行为写'本回合无辅助投掷'>",
      "opponent_predict": "<对面战术预测：基于 opponent_context.predicted_intent 和 evidence，评价你是否预判到了对手意图。要点：① 对手近 3 回合趋势（哪个包点/哪种经济）；② 你本回合站位是否对应该预判；③ 如果预判错了，复盘信号点>",
      "adjustment":       "<根据对方战术动态调整：评价你和团队对对手意图的应对。要点：① 对手是高经济还是低经济，你的站位/激进度是否匹配；② 对手 rush 时你方是否提前堆点；③ 对手默认时你方是否抢先控图。给出'下次该如何应对'的可执行建议>",
      "verdict": "<执行/失误/亮眼/平淡 中选一个>"
    }
  ],
  "pro_reference":"<对比的职业选手参考语>"
}

【硬性规则 — 不遵守即不合格】
1. **本场地图是 prompt 里给定的那张**。所有建议、点位、训练方向**严禁**提到其它地图（dust2/mirage/inferno/ancient 等不能跨提）。如果给的是 ancient，就只能说 ancient 的位置（A 大房、B 坡、洞穴、连接处等）。
2. **战术术语全部用中文**。允许出现的局型只有这五个：手枪局、经济局、强起局、半起局、全装局。残局用"残局"或"1vN"，禁止 clutch。**输出里任何英文经济/局型术语视为不合格，会被自动剔除并降级为离线规则版**。武器名（USP-S/AK47 等）可保留英文。
3. **strengths/weaknesses 各 3-4 条，suggestions 3-4 条，detail 50 字以内**。
4. **round_analyses 覆盖 6-10 个关键回合**（首杀/末杀/残局/连续阵亡/经济翻盘/全队战术失败回合），不逐回合列。
5. **tactic 字段必须包含三要素**：① 全队战术意图（推测：例如"T 方打 A 大坑爆点"）② 你的实际动作 vs 战术意图（执行/脱节/单干）③ 引用至少一个具体位置名 + 至少一个队友名字。
6. clutch 字段仅当 clutch_situation 非空时才输出。
7. **grenade_eval/map_control/utility_assist/opponent_predict/adjustment 五个新字段必须从对应数据中引用具体数字或位置**：例如 grenade_eval 必须写"在 X 位置投出的烟，造成 N 伤害"，map_control 必须写"control_score=N，主要在 X zone 占 N%"，opponent_predict 必须引用 evidence 中的回合号。**严禁泛泛而谈**。
8. **aim_eval/pressure_eval/team_sync_eval/smoke_eval/movement_eval 五个顶层评注必须引用 prompt 里对应章节的具体数字**（距离分布、KAST 对比、trade 率、烟雾准确率、跑动开枪率），如果对应数据缺失就写"本场样本不足以做 X 评估"。
9. 如果某个字段对应的数据完全为空（例如本回合 grenades=[]），就写"本回合无 X 数据"，不要瞎编。
10. 用第二人称"你"，直接、专业、不绕弯。中文回答。`

func buildPrompt(stats domain.MatchStats, baseline domain.ProBaseline, cmp []domain.MetricCompare) string {
	var b strings.Builder
	t := stats.Target
	fmt.Fprintf(&b, "## 比赛信息\n地图: %s（请只针对这张地图给建议，不要跨提其它地图）\n", stats.Map)
	fmt.Fprintf(&b, "点位规范: %s\n", callouts.PromptReference(stats.Map))
	fmt.Fprintf(&b, "比分: T %d - %d CT, 总回合: %d, 时长: %ds\n\n",
		stats.ScoreT, stats.ScoreCT, stats.RoundsTotal, stats.DurationSec)

	fmt.Fprintf(&b, "## 目标玩家 %s [%s]\n", t.Name, t.Team)
	fmt.Fprintf(&b, "K/D/A: %d/%d/%d, HS%%: %.1f%%, ADR: %.1f, KAST: %.1f%%\n",
		t.Kills, t.Deaths, t.Assists, t.HeadshotPct, t.ADR, t.KAST)
	fmt.Fprintf(&b, "投掷物伤害: %d, 闪光助攻: %d\n", t.UtilityDamage, t.FlashAssists)

	if len(t.WeaponKills) > 0 {
		fmt.Fprintf(&b, "武器击杀分布: ")
		for w, n := range t.WeaponKills {
			fmt.Fprintf(&b, "%s=%d ", w, n)
		}
		b.WriteString("\n")
	}
	if len(t.GrenadeUsage) > 0 {
		fmt.Fprintf(&b, "投掷物使用: ")
		for g, n := range t.GrenadeUsage {
			fmt.Fprintf(&b, "%s=%d ", g, n)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "\n## 队友\n")
	for _, m := range stats.Teammates {
		fmt.Fprintf(&b, "- %s K/D=%d/%d ADR=%.0f KAST=%.0f%%\n", m.Name, m.Kills, m.Deaths, m.ADR, m.KAST)
	}
	fmt.Fprintf(&b, "\n## 对手\n")
	for _, m := range stats.Opponents {
		fmt.Fprintf(&b, "- %s K/D=%d/%d ADR=%.0f KAST=%.0f%%\n", m.Name, m.Kills, m.Deaths, m.ADR, m.KAST)
	}

	fmt.Fprintf(&b, "\n## 职业选手基线（%s · %s）\n", baseline.Role, baseline.Map)
	fmt.Fprintf(&b, "ADR: %.1f, KAST: %.1f%%, HS%%: %.1f%%, 投掷伤害: %d\n备注: %s\n",
		baseline.ADR, baseline.KAST, baseline.HeadshotPct, baseline.UtilityDamage, baseline.Notes)

	fmt.Fprintf(&b, "\n## 数据对比\n")
	for _, c := range cmp {
		fmt.Fprintf(&b, "- %s: 你 %.1f vs 职业 %.1f → %s\n", c.Metric, c.You, c.ProMedian, c.Verdict)
	}

	if a := stats.Target.AimQuality; a != nil {
		fmt.Fprintf(&b, "\n## 枪法画像\n")
		fmt.Fprintf(&b, "距离分布: 近=%d 中=%d 远=%d, 平均距离=%.1fm\n", a.CloseKills, a.MidKills, a.LongKills, a.AvgKillDistance)
		fmt.Fprintf(&b, "近 HS%%=%.0f%% 远 HS%%=%.0f%%, 多杀回合=%d, 最快连杀=%.2fs\n",
			a.HSCloseRate, a.HSLongRate, a.MultiKillRounds, a.FastestDoubleSec)
		fmt.Fprintf(&b, "首杀对枪 胜=%d 负=%d\n", a.OpeningDuelWins, a.OpeningDuelLoss)
	}

	if p := stats.Target.Pressure; p != nil {
		fmt.Fprintf(&b, "\n## 压力下表现\n")
		fmt.Fprintf(&b, "高压回合=%d (KAST %.0f%%) vs 普通回合=%d (KAST %.0f%%)\n",
			p.HighStakeRounds, p.HighStakeKAST, p.NormalRounds, p.NormalKAST)
		fmt.Fprintf(&b, "残局打/赢=%d/%d, 经济局击杀=%d, 赛点回合=%d, 最大连败=%d\n",
			p.ClutchPlayed, p.ClutchWon, p.EcoRoundKills, p.MatchPointRounds, p.LosingStreakMax)
	}

	if t := stats.TeamSync; t != nil {
		fmt.Fprintf(&b, "\n## 团队配合\n")
		fmt.Fprintf(&b, "trade 击杀=%d trade 死亡=%d (trade率%.0f%%), 平均 trade 间隔=%.2fs\n",
			t.TradeKills, t.TradeDeaths, t.TradeRate, t.AvgTradeGapSec)
		fmt.Fprintf(&b, "跟进击杀=%d, 孤立死=%d, 全队堆点死回合=%d\n",
			t.FollowupKills, t.SoloDeaths, t.StackedDeathRounds)
		if t.BestTradePartner != "" {
			fmt.Fprintf(&b, "最佳搭档: %s (%d 次同回合配合)\n", t.BestTradePartner, t.BestPartnerCount)
		}
	}

	if sr := stats.SmokeReport; sr != nil {
		fmt.Fprintf(&b, "\n## 烟雾覆盖率\n")
		fmt.Fprintf(&b, "投出%d颗，命中关键封烟点%d颗(准确率%.0f%%)，漏掉%d个关键控点\n",
			sr.TotalSmokes, sr.AccurateSmokes, sr.AccuracyPct, sr.KeyChokeMissed)
		if len(sr.PerSmokeNotes) > 0 {
			fmt.Fprintf(&b, "明细: ")
			for i, n := range sr.PerSmokeNotes {
				if i >= 6 {
					break
				}
				fmt.Fprintf(&b, "[R%d %s→%s %s] ", n.Round, fmt.Sprintf("%.0fs", n.TimeSec), n.LandingZone, n.Verdict)
			}
			b.WriteString("\n")
		}
	}

	if md := stats.Movement; md != nil {
		fmt.Fprintf(&b, "\n## 移动纪律\n")
		fmt.Fprintf(&b, "跑动开枪率=%.0f%%(>30%%偏高), 平均开枪速度=%.0f, 早期突进死=%d, 孤立首死=%d\n",
			md.MovingKillRate, md.AvgKillerSpeed, md.OvercommitDeaths, md.IsolationDeaths)
	}

	if len(stats.Rounds) > 0 {
		fmt.Fprintf(&b, "\n## 回合时间轴 + 战术上下文（共 %d 回合）\n", len(stats.Rounds))
		fmt.Fprintf(&b, "（下方经济列已全部中文化，你的输出必须沿用中文，不得再翻回英文）\n\n")
		for _, r := range stats.Rounds {
			parts := []string{fmt.Sprintf("R%d %s胜(%s)", r.Number, r.WinnerTeam, r.EndReason)}
			parts = append(parts, fmt.Sprintf("经济:你方=%s/装备$%d 对手=%s/装备$%d",
				econCN(r.TargetTeamEcon), r.TargetTeamEquipValue, econCN(r.OpponentTeamEcon), r.OpponentTeamEquipValue))
			if r.FirstContactSec > 0 {
				parts = append(parts, fmt.Sprintf("首接触%.0fs", r.FirstContactSec))
			}
			if r.TargetKills > 0 {
				parts = append(parts, fmt.Sprintf("你%dkill", r.TargetKills))
			}
			if r.TargetDied {
				zone := r.TargetDeathZone
				if zone == "" {
					zone = "未知点位"
				}
				parts = append(parts, fmt.Sprintf("你阵亡@%.0fs[%s]", r.TargetDeathTimeSec-r.StartTimeSec, zone))
			} else {
				parts = append(parts, "你存活")
			}
			if len(r.TeamPushZones) > 0 {
				parts = append(parts, "队友死位:["+strings.Join(r.TeamPushZones, ",")+"]")
			}
			if r.TeamFirstDeath != "" {
				parts = append(parts, "队首死:"+r.TeamFirstDeath)
			}
			if r.BombPlanted {
				bombInfo := "炸弹安放"
				if r.BombSiteZone != "" {
					bombInfo += "@" + r.BombSiteZone
				}
				if r.BombDefused {
					bombInfo += "→拆除"
				}
				parts = append(parts, bombInfo)
			}
			if r.ClutchSituation != "" {
				parts = append(parts, "残局"+r.ClutchSituation+"="+r.ClutchOutcome)
			}
			if len(r.TargetEvents) > 0 {
				ws := []string{}
				for _, e := range r.TargetEvents {
					tag := e.Weapon
					if e.Headshot {
						tag += "(HS)"
					}
					if e.VictimZone != "" {
						tag += "@" + e.VictimZone
					}
					ws = append(ws, tag)
				}
				parts = append(parts, "你的击杀:["+strings.Join(ws, ",")+"]")
			}
			if r.TargetActions != nil {
				if len(r.TargetActions.Grenades) > 0 {
					gtags := []string{}
					for _, g := range r.TargetActions.Grenades {
						tag := fmt.Sprintf("%s@%.0fs", g.Type, g.ThrownAtSec)
						if g.LandingZone != "" {
							tag += "→" + g.LandingZone
						}
						if g.DamageDealt > 0 {
							tag += fmt.Sprintf("(伤害%d)", g.DamageDealt)
						}
						if g.EnemiesFlashed > 0 {
							tag += fmt.Sprintf("(闪敌%d", g.EnemiesFlashed)
							if g.FlashDuration > 0 {
								tag += fmt.Sprintf(",%.1fs", g.FlashDuration)
							}
							tag += ")"
						}
						if g.TeamFlashed > 0 {
							tag += fmt.Sprintf("(闪队%d)", g.TeamFlashed)
						}
						gtags = append(gtags, tag)
					}
					parts = append(parts, "你的道具:["+strings.Join(gtags, ",")+"]")
				}
				if len(r.TargetActions.PositionTrack) > 0 {
					ps := []string{}
					for _, ps2 := range r.TargetActions.PositionTrack {
						ps = append(ps, fmt.Sprintf("%.0fs:%s", ps2.TimeSec, ps2.Zone))
					}
					parts = append(parts, "走位:["+strings.Join(ps, "→")+"]")
				}
				if len(r.TargetActions.ZoneOccupancy) > 0 {
					arr := []zonePct{}
					for z, pct := range r.TargetActions.ZoneOccupancy {
						arr = append(arr, zonePct{z, pct})
					}
					sortByPctDesc(arr)
					top := []string{}
					for i, x := range arr {
						if i >= 3 {
							break
						}
						top = append(top, fmt.Sprintf("%s=%.0f%%", x.z, x.p))
					}
					parts = append(parts, fmt.Sprintf("控图分%d|占比[%s]", r.TargetActions.ControlScore, strings.Join(top, ",")))
				}
				if len(r.TargetActions.FlashAssists) > 0 {
					fs := []string{}
					for _, f := range r.TargetActions.FlashAssists {
						fs = append(fs, fmt.Sprintf("%s@%s", f.VictimName, f.VictimZone))
					}
					parts = append(parts, "你闪到:["+strings.Join(fs, ",")+"]")
				}
				if r.TargetActions.UtilityDamage > 0 {
					parts = append(parts, fmt.Sprintf("道具伤害%d", r.TargetActions.UtilityDamage))
				}
			}
			if r.OpponentContext != nil {
				if r.OpponentContext.PredictedIntent != "" {
					parts = append(parts, "对手预判:["+r.OpponentContext.PredictedIntent+"]")
				}
				if r.OpponentContext.PredictionEvidence != "" {
					parts = append(parts, "依据:["+r.OpponentContext.PredictionEvidence+"]")
				}
			}
			if r.Timeline != nil {
				tl := r.Timeline
				parts = append(parts, fmt.Sprintf("阶段=%s|节奏=%s", tl.Phase, tl.PaceProfile))
				if len(tl.KeyEvents) > 0 {
					evs := []string{}
					for i, e := range tl.KeyEvents {
						if i >= 6 {
							break
						}
						tag := fmt.Sprintf("%.0fs %s", e.TimeSec, e.Kind)
						if e.Detail != "" {
							tag += ":" + e.Detail
						}
						evs = append(evs, tag)
					}
					parts = append(parts, "时间轴:["+strings.Join(evs, "→")+"]")
				}
			}
			fmt.Fprintln(&b, strings.Join(parts, " | "))
		}
	}

	b.WriteString("\n请基于以上数据生成结构化点评 JSON。重点：每条 round_analyses 必须从全队战术意图（推测）讲起，对照你的实际动作，引用具体地图位置和队友名字。所有建议只能针对当前地图，禁止跨地图举例。")
	return b.String()
}

func econCN(e string) string {
	switch e {
	case "pistol":
		return "手枪局"
	case "eco":
		return "经济局"
	case "force":
		return "强起局"
	case "semi":
		return "半起局"
	case "full":
		return "全装局"
	}
	return e
}

func buildComparison(p domain.PlayerStats, b domain.ProBaseline) []domain.MetricCompare {
	mk := func(name string, you, pro float64) domain.MetricCompare {
		v := "持平"
		ratio := 0.0
		if pro > 0 {
			ratio = (you - pro) / pro
		}
		switch {
		case ratio >= 0.1:
			v = "优于职业基线"
		case ratio <= -0.15:
			v = "差距明显"
		case ratio < 0:
			v = "略低于基线"
		}
		return domain.MetricCompare{Metric: name, You: you, ProMedian: pro, Verdict: v}
	}
	return []domain.MetricCompare{
		mk("ADR", p.ADR, b.ADR),
		mk("KAST%", p.KAST, b.KAST),
		mk("HS%", p.HeadshotPct, b.HeadshotPct),
		mk("UtilityDamage", float64(p.UtilityDamage), float64(b.UtilityDamage)),
	}
}

func parseLLMReport(raw string) (domain.AnalysisReport, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "{"); i > 0 {
		raw = raw[i:]
	}
	last := strings.LastIndex(raw, "}")
	if last >= 0 && last < len(raw)-1 {
		raw = raw[:last+1]
	}
	var r domain.AnalysisReport
	if err := json.Unmarshal([]byte(raw), &r); err == nil {
		return r, nil
	}
	repaired := repairTruncatedJSON(raw)
	if repaired != raw {
		var r2 domain.AnalysisReport
		if err := json.Unmarshal([]byte(repaired), &r2); err == nil {
			return r2, nil
		}
	}
	var empty domain.AnalysisReport
	return empty, json.Unmarshal([]byte(raw), &empty)
}

func repairTruncatedJSON(s string) string {
	if !strings.HasPrefix(strings.TrimSpace(s), "{") {
		return s
	}
	stack := []byte{}
	inStr := false
	escape := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inStr {
			escape = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch c {
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if len(stack) == 0 && !inStr {
		return s
	}
	out := s
	if inStr {
		out += `"`
	}
	for i := len(stack) - 1; i >= 0; i-- {
		out += string(stack[i])
	}
	return out
}

func offlineReport(demoID string, stats domain.MatchStats, baseline domain.ProBaseline, cmp []domain.MetricCompare) domain.AnalysisReport {
	t := stats.Target
	r := domain.AnalysisReport{
		DemoID:       demoID,
		GeneratedAt:  time.Now().UTC(),
		Comparison:   cmp,
		ProReference: baseline.Notes + "（离线模式：未调用 LLM，仅基于规则）",
	}

	kd := 0.0
	if t.Deaths > 0 {
		kd = float64(t.Kills) / float64(t.Deaths)
	}
	score := 50
	score += int((t.ADR - baseline.ADR) / 2)
	score += int((t.KAST - baseline.KAST) / 3)
	if kd > 1.2 {
		score += 5
	}
	if score < 10 {
		score = 10
	}
	if score > 95 {
		score = 95
	}
	r.OverallScore = score
	r.Verdict = fmt.Sprintf("本场 K/D=%.2f，ADR=%.1f，KAST=%.1f%%。对比职业%s基线：%s。",
		kd, t.ADR, t.KAST, roleDisplayName(baseline.Role), overallVerdict(cmp))

	if t.HeadshotPct >= baseline.HeadshotPct {
		r.Strengths = append(r.Strengths, domain.ReportPoint{
			Title:  "枪法精度优秀",
			Detail: fmt.Sprintf("HS%% %.1f%% 达到甚至超过职业基线 %.1f%%，瞄准位置稳定", t.HeadshotPct, baseline.HeadshotPct),
		})
	}
	if t.KAST >= baseline.KAST {
		r.Strengths = append(r.Strengths, domain.ReportPoint{
			Title:  "回合参与度高",
			Detail: fmt.Sprintf("KAST %.1f%% 高于基线 %.1f%%，对团队贡献稳定", t.KAST, baseline.KAST),
		})
	}
	if t.UtilityDamage >= baseline.UtilityDamage {
		r.Strengths = append(r.Strengths, domain.ReportPoint{
			Title:  "投掷物利用合格",
			Detail: fmt.Sprintf("投掷伤害 %d 达到基线 %d", t.UtilityDamage, baseline.UtilityDamage),
		})
	}
	if len(r.Strengths) == 0 {
		r.Strengths = []domain.ReportPoint{{Title: "保持稳定", Detail: "无明显高光也无明显短板，处于成长期"}}
	}

	if t.ADR < baseline.ADR*0.85 {
		r.Weaknesses = append(r.Weaknesses, domain.ReportPoint{
			Title:  "ADR 偏低",
			Detail: fmt.Sprintf("ADR %.1f 显著低于基线 %.1f，输出贡献不足", t.ADR, baseline.ADR),
		})
	}
	if t.KAST < baseline.KAST*0.9 {
		r.Weaknesses = append(r.Weaknesses, domain.ReportPoint{
			Title:  "回合参与度不足",
			Detail: fmt.Sprintf("KAST %.1f%% 低于基线 %.1f%%，部分回合空白", t.KAST, baseline.KAST),
		})
	}
	if t.UtilityDamage < baseline.UtilityDamage/2 {
		r.Weaknesses = append(r.Weaknesses, domain.ReportPoint{
			Title:  "投掷物使用浪费",
			Detail: fmt.Sprintf("投掷伤害仅 %d，远低于基线 %d", t.UtilityDamage, baseline.UtilityDamage),
		})
	}
	if len(r.Weaknesses) == 0 {
		r.Weaknesses = []domain.ReportPoint{{Title: "无显著短板", Detail: "整体数据均衡，可冲击下一档"}}
	}

	r.Suggestions = []domain.ReportPoint{
		{Title: "针对" + roleDisplayName(baseline.Role) + "角色训练", Detail: baseline.Notes},
		{Title: "复盘 " + stats.Map + " 地图位置", Detail: "对照职业选手在 " + stats.Map + " 上的站位与投掷物使用，禁止跨地图借鉴"},
	}

	r.RoundAnalyses = ruleBasedRoundAnalyses(stats)
	return r
}

func roleDisplayName(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "rifler":
		return "步枪手"
	case "awper":
		return "AWP手"
	case "entry":
		return "突破手"
	case "support":
		return "辅助位"
	case "igl":
		return "指挥位"
	default:
		if role == "" {
			return "选手"
		}
		return role
	}
}

func ruleBasedRoundAnalyses(stats domain.MatchStats) []domain.RoundAnalysisOut {
	out := []domain.RoundAnalysisOut{}
	for _, rd := range stats.Rounds {
		ra, ok := buildOfflineRoundAnalysis(stats, rd)
		if !ok {
			continue
		}
		out = append(out, ra)
	}
	return out
}
func buildOfflineRoundAnalysis(stats domain.MatchStats, rd domain.RoundSummary) (domain.RoundAnalysisOut, bool) {
	myEcon := econCN(rd.TargetTeamEcon)
	oppEcon := econCN(rd.OpponentTeamEcon)
	equipDiff := rd.TargetTeamEquipValue - rd.OpponentTeamEquipValue

	pushZones := strings.Join(stripZoneCount(rd.TeamPushZones), "、")
	primaryDir := primaryPushZone(rd.TeamPushZones)

	earlyDeath := rd.TargetDied && rd.TargetDeathTimeSec-rd.StartTimeSec < 25
	deathSec := 0.0
	if rd.TargetDied {
		deathSec = rd.TargetDeathTimeSec - rd.StartTimeSec
	}

	verdict := "平淡"
	switch {
	case rd.ClutchSituation != "" && strings.Contains(rd.ClutchOutcome, "你赢下"):
		verdict = "亮眼"
	case rd.ClutchSituation != "":
		verdict = "残局"
	case rd.TargetKills >= 3:
		verdict = "亮眼"
	case earlyDeath:
		verdict = "失误"
	case rd.TargetKills >= 1:
		verdict = "执行"
	}

	var tactic strings.Builder
	fmt.Fprintf(&tactic, "本回合你方为%s（装备$%d）, 对手为%s（装备$%d）", myEcon, rd.TargetTeamEquipValue, oppEcon, rd.OpponentTeamEquipValue)
	switch {
	case equipDiff <= -10000:
		tactic.WriteString("，火力差距悬殊，本回合定位为消耗或抢枪")
	case equipDiff <= -3000:
		tactic.WriteString("，装备处于劣势，正面硬刚不利")
	case equipDiff >= 10000:
		tactic.WriteString("，装备压制对手，应主动找会战")
	}
	tactic.WriteString("。")

	if primaryDir != "" {
		fmt.Fprintf(&tactic, "全队主攻方向看队友死位集中在%s，意图大概率是%s", pushZones, inferIntent(stats.Map, primaryDir))
		if rd.TeamFirstDeath != "" {
			fmt.Fprintf(&tactic, "（首死%s）", rd.TeamFirstDeath)
		}
		tactic.WriteString("；")
	} else if rd.TeamFirstDeath != "" {
		fmt.Fprintf(&tactic, "队伍首死在%s，", rd.TeamFirstDeath)
	}

	switch {
	case rd.TargetDied && rd.TargetDeathZone != "" && primaryDir != "" && !strings.Contains(rd.TargetDeathZone, primaryDir):
		fmt.Fprintf(&tactic, "你却阵亡于%s，与全队进攻方向脱节，建议下次跟上队友节奏", rd.TargetDeathZone)
	case rd.TargetDied && rd.TargetDeathZone != "":
		fmt.Fprintf(&tactic, "你%.0fs 阵亡于%s，时机和站位需要复盘", deathSec, rd.TargetDeathZone)
	case rd.TargetKills >= 2 && primaryDir != "":
		fmt.Fprintf(&tactic, "你打出 %d kill 配合队伍%s方向推进，执行到位", rd.TargetKills, primaryDir)
	case rd.TargetKills >= 1:
		fmt.Fprintf(&tactic, "你打出 %d kill 跟住团队节奏", rd.TargetKills)
	default:
		tactic.WriteString("你没有击杀贡献，建议加强卡点或投掷物配合")
	}
	tactic.WriteString("。")

	ra := domain.RoundAnalysisOut{
		Round:   rd.Number,
		Tactic:  tactic.String(),
		Verdict: verdict,
	}

	switch {
	case earlyDeath:
		zone := rd.TargetDeathZone
		if zone == "" {
			zone = "未知点位"
		}
		ra.Mistake = fmt.Sprintf("开局 %.0fs 抢点失败阵亡于%s，前期信息没拿到反而把人头送给对手", deathSec, zone)
	case rd.TargetDied && primaryDir != "" && rd.TargetDeathZone != "" && !strings.Contains(rd.TargetDeathZone, primaryDir):
		ra.Mistake = fmt.Sprintf("队伍主攻%s，你单走至%s阵亡，方向脱节导致以多打少机会丢失", primaryDir, rd.TargetDeathZone)
	case equipDiff >= 5000 && rd.TargetKills == 0 && rd.TargetDied:
		ra.Mistake = "装备占优却没拿下击杀，会战节奏没把握住"
	}

	if rd.ClutchSituation != "" {
		who := "你"
		if !strings.Contains(rd.ClutchOutcome, "你") {
			who = strings.SplitN(rd.ClutchOutcome, "赢", 2)[0]
			who = strings.SplitN(who, "输", 2)[0]
		}
		switch {
		case strings.Contains(rd.ClutchOutcome, "你赢下"):
			ra.Clutch = fmt.Sprintf("你%s拉满，思路正确：拆解多人优先打孤立点位，利用炸弹/时间分摊敌方注意力", rd.ClutchSituation)
		case strings.Contains(rd.ClutchOutcome, "你输掉"):
			ra.Clutch = fmt.Sprintf("你%s告负，复盘点：是否过早暴露位置/没用拐角分散对手", rd.ClutchSituation)
		default:
			ra.Clutch = fmt.Sprintf("%s打%s，结果：%s", who, rd.ClutchSituation, rd.ClutchOutcome)
		}
	}

	ra.GrenadeEval = offlineGrenadeEval(rd.TargetActions)
	ra.MapControl = offlineMapControl(rd.TargetActions)
	ra.UtilityAssist = offlineUtilityAssist(rd.TargetActions)
	ra.OpponentPredict = offlineOpponentPredict(rd.OpponentContext)
	ra.Adjustment = offlineAdjustment(stats.Map, rd, rd.OpponentContext)

	if ra.Tactic == "" && ra.Mistake == "" && ra.Clutch == "" && len(rd.TargetEvents) == 0 {
		return ra, false
	}
	return ra, true
}

func offlineGrenadeEval(ta *domain.TargetRoundActions) string {
	if ta == nil || len(ta.Grenades) == 0 {
		return "本回合无道具使用"
	}
	good, waste := 0, 0
	parts := []string{}
	for _, g := range ta.Grenades {
		tag := fmt.Sprintf("%s→%s", grenadeCN(g.Type), g.LandingZone)
		if g.DamageDealt >= 20 {
			tag += fmt.Sprintf("(伤害%d，到位)", g.DamageDealt)
			good++
		} else if g.EnemiesFlashed >= 2 {
			tag += fmt.Sprintf("(闪%d敌，到位)", g.EnemiesFlashed)
			good++
		} else if g.TeamFlashed >= 1 {
			tag += fmt.Sprintf("(闪%d队友，浪费)", g.TeamFlashed)
			waste++
		} else if g.DamageDealt == 0 && g.EnemiesFlashed == 0 {
			tag += "(无效果)"
			waste++
		} else {
			tag += "(一般)"
		}
		parts = append(parts, tag)
	}
	verdict := ""
	switch {
	case good >= 2 && waste == 0:
		verdict = "整体到位"
	case waste >= good:
		verdict = "整体浪费多于到位"
	default:
		verdict = "效果一般"
	}
	return strings.Join(parts, "；") + "；" + verdict
}

func offlineMapControl(ta *domain.TargetRoundActions) string {
	if ta == nil || (len(ta.ZoneOccupancy) == 0 && len(ta.PositionTrack) == 0) {
		return "本回合走位数据不足"
	}
	level := "被动卡点"
	switch {
	case ta.ControlScore >= 60:
		level = "高强度控图"
	case ta.ControlScore >= 30:
		level = "中等控图"
	}
	arr := []zonePct{}
	for z, p := range ta.ZoneOccupancy {
		arr = append(arr, zonePct{z, p})
	}
	sortByPctDesc(arr)
	tops := []string{}
	for i, x := range arr {
		if i >= 2 {
			break
		}
		tops = append(tops, fmt.Sprintf("%s占%.0f%%", x.z, x.p))
	}
	out := fmt.Sprintf("控图分 %d（%s）", ta.ControlScore, level)
	if len(tops) > 0 {
		out += "，主要在 " + strings.Join(tops, "/")
	}
	if len(ta.PositionTrack) >= 2 {
		first := ta.PositionTrack[0]
		last := ta.PositionTrack[len(ta.PositionTrack)-1]
		if first.Zone != last.Zone {
			out += fmt.Sprintf("，从%s移动到%s", first.Zone, last.Zone)
		} else {
			out += fmt.Sprintf("，全程蹲在%s", first.Zone)
		}
	}
	return out
}

func offlineUtilityAssist(ta *domain.TargetRoundActions) string {
	if ta == nil {
		return "本回合无辅助投掷"
	}
	enemyFlashed, teamFlashed := 0, 0
	for _, g := range ta.Grenades {
		enemyFlashed += g.EnemiesFlashed
		teamFlashed += g.TeamFlashed
	}
	if enemyFlashed == 0 && teamFlashed == 0 && len(ta.FlashAssists) == 0 && ta.UtilityDamage == 0 {
		return "本回合无辅助投掷行为"
	}
	parts := []string{}
	if enemyFlashed > 0 {
		parts = append(parts, fmt.Sprintf("闪到敌方 %d 人次", enemyFlashed))
	}
	if teamFlashed > 0 {
		parts = append(parts, fmt.Sprintf("闪到队友 %d 人次（负面）", teamFlashed))
	}
	if ta.UtilityDamage > 0 {
		parts = append(parts, fmt.Sprintf("投掷伤害 %d", ta.UtilityDamage))
	}
	verdict := ""
	switch {
	case teamFlashed > enemyFlashed:
		verdict = "扰队友多于扰对手，时机/角度需要复盘"
	case enemyFlashed >= 2:
		verdict = "辅助到位，配合队友推进"
	case enemyFlashed >= 1 || ta.UtilityDamage >= 30:
		verdict = "有效辅助"
	default:
		verdict = "辅助效果有限"
	}
	return strings.Join(parts, "，") + "；" + verdict
}

func offlineOpponentPredict(oc *domain.OpponentContext) string {
	if oc == nil {
		return "对手数据不足，难以做近期趋势预测"
	}
	out := oc.PredictedIntent
	if oc.PredictionEvidence != "" {
		out += "（依据：" + oc.PredictionEvidence + "）"
	}
	return out
}

func offlineAdjustment(mapName string, rd domain.RoundSummary, oc *domain.OpponentContext) string {
	if oc == nil {
		return "对手节奏未明确，建议默认控图先听枪声判断"
	}
	mz := mapZoneNames(mapName)
	parts := []string{}
	switch rd.OpponentTeamEcon {
	case "eco":
		parts = append(parts, "对手经济局，应主动压点切对方信息，不要给救枪机会")
	case "force":
		parts = append(parts, "对手强起，会集中一路 rush，CT 提前堆点听 rush 路线")
	case "full":
		parts = append(parts, "对手满装，会执行完整战术，控好默认+留烟应对包点")
	case "semi":
		parts = append(parts, "对手半起，进攻欲望中等，关注首接触点位")
	}
	intent := strings.ToLower(oc.PredictedIntent)
	switch {
	case strings.Contains(intent, "压 a") || strings.Contains(intent, "打 a"):
		parts = append(parts, fmt.Sprintf("对手近期压 A，CT 半场 A 多堆 1 人 + 留烟封 %s", mz.aChoke))
	case strings.Contains(intent, "压 b") || strings.Contains(intent, "打 b"):
		parts = append(parts, fmt.Sprintf("对手近期压 B，CT 半场 B 多堆 1 人 + %s 留烟", mz.bChoke))
	case strings.Contains(intent, "分散") || strings.Contains(intent, "动态"):
		parts = append(parts, "对手打法分散，建议 1-3-1 默认控图，听首接触再轮转")
	}
	if len(parts) == 0 {
		return "本回合无显著调整空间，按默认体系执行"
	}
	return strings.Join(parts, "；")
}

type mapZoneRefs struct {
	aChoke string // A 路关键封烟点
	bChoke string // B 路关键封烟点
}

func mapZoneNames(mapName string) mapZoneRefs {
	m := strings.ToLower(strings.TrimPrefix(strings.ToLower(mapName), "de_"))
	switch m {
	case "dust2":
		return mapZoneRefs{aChoke: "A 长/A 大坑", bChoke: "B 隧道/B 门"}
	case "mirage":
		return mapZoneRefs{aChoke: "A 短/A 大坑", bChoke: "B 公寓/B 门"}
	case "inferno":
		return mapZoneRefs{aChoke: "A 短/A 木", bChoke: "香蕉道/B 阳台"}
	case "nuke":
		return mapZoneRefs{aChoke: "外场/A 平台", bChoke: "B 下层入口"}
	case "ancient":
		return mapZoneRefs{aChoke: "A 大房/A 坡道", bChoke: "B 坡/B 底"}
	case "anubis":
		return mapZoneRefs{aChoke: "A 街/A 连接", bChoke: "B 水道/广场"}
	case "overpass":
		return mapZoneRefs{aChoke: "A 长/A 连接", bChoke: "B 隧道/B 门"}
	case "vertigo":
		return mapZoneRefs{aChoke: "A 坡道/A 楼梯", bChoke: "B 坡道/B 入口"}
	}
	return mapZoneRefs{aChoke: "A 路关键控点", bChoke: "B 路关键控点"}
}

func grenadeCN(t string) string {
	switch t {
	case "smoke":
		return "烟雾弹"
	case "flash":
		return "闪光弹"
	case "he":
		return "高爆弹"
	case "molotov":
		return "燃烧弹"
	}
	return t
}

func stripZoneCount(zs []string) []string {
	out := make([]string, 0, len(zs))
	for _, z := range zs {
		if i := strings.Index(z, "×"); i > 0 {
			out = append(out, z[:i])
		} else {
			out = append(out, z)
		}
	}
	return out
}

func primaryPushZone(zs []string) string {
	if len(zs) == 0 {
		return ""
	}
	z := zs[0]
	if i := strings.Index(z, "×"); i > 0 {
		z = z[:i]
	}
	return z
}

func inferIntent(mapName, zone string) string {
	z := strings.ToLower(zone)
	switch {
	case strings.Contains(z, "a") && (strings.Contains(z, "大") || strings.Contains(z, "长") || strings.Contains(z, "点")):
		return "打 A 包点 / 爆 A"
	case strings.Contains(z, "b") && (strings.Contains(z, "点") || strings.Contains(z, "门") || strings.Contains(z, "坡")):
		return "打 B 包点 / 爆 B"
	case strings.Contains(z, "中") || strings.Contains(z, "洞") || strings.Contains(z, "连接"):
		return "中路控制 / split 准备"
	case strings.Contains(z, "香蕉"):
		return "香蕉道压制 / B 进攻"
	}
	return "建立地图控制"
}

func overallVerdict(cmp []domain.MetricCompare) string {
	better, worse := 0, 0
	for _, c := range cmp {
		switch c.Verdict {
		case "优于职业基线":
			better++
		case "差距明显":
			worse += 2
		case "略低于基线":
			worse++
		}
	}
	if better > worse {
		return "表现优秀"
	}
	if worse > better+1 {
		return "存在明显差距"
	}
	return "处于及格线"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

type zonePct struct {
	z string
	p float64
}

func sortByPctDesc(arr []zonePct) {
	for i := 1; i < len(arr); i++ {
		for j := i; j > 0 && arr[j].p > arr[j-1].p; j-- {
			arr[j], arr[j-1] = arr[j-1], arr[j]
		}
	}
}
