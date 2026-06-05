package parser

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	demoinfocs "github.com/markus-wa/demoinfocs-golang/v5/pkg/demoinfocs"
	"github.com/markus-wa/demoinfocs-golang/v5/pkg/demoinfocs/common"
	events "github.com/markus-wa/demoinfocs-golang/v5/pkg/demoinfocs/events"
	"github.com/markus-wa/demoinfocs-golang/v5/pkg/demoinfocs/msg"

	"github.com/cs2demo/platform/internal/callouts"
	"github.com/cs2demo/platform/internal/domain"
)

type TargetNotFoundError struct {
	Hint       string
	Candidates []string
}

func (e *TargetNotFoundError) Error() string {
	return fmt.Sprintf("target player %q not found; candidates: %s", e.Hint, strings.Join(e.Candidates, ", "))
}

func IsTargetNotFound(err error) (*TargetNotFoundError, bool) {
	var tnf *TargetNotFoundError
	if errors.As(err, &tnf) {
		return tnf, true
	}
	return nil, false
}

type Parser struct{}

func New() *Parser { return &Parser{} }

func (p *Parser) ListPlayers(path string) ([]string, error) {
	stats, err := p.Parse(path, "\x00__inspect_players__")
	if tnf, ok := IsTargetNotFound(err); ok {
		return tnf.Candidates, nil
	}
	if err != nil {
		return nil, err
	}
	return stats.AllPlayers, nil
}

func (p *Parser) Parse(path, targetUser string) (domain.MatchStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return domain.MatchStats{}, fmt.Errorf("open demo: %w", err)
	}
	defer f.Close()

	parser := demoinfocs.NewParser(f)
	defer parser.Close()

	state := &parseState{
		targetName:         strings.ToLower(strings.TrimSpace(targetUser)),
		players:            map[uint64]*domain.PlayerStats{},
		damage:             map[uint64]int{},
		kastRound:          map[int]map[uint64]bool{},
		aliveTargetTeam:    map[int]int{},
		aliveOpponentTeam:  map[int]int{},
		teamDeathOrder:     map[int][]string{},
		teamPushZones:      map[int]map[string]int{},
		targetGrenades:     map[int][]domain.GrenadeEvent{},
		grenadeByEntity:    map[int]*pendingGrenade{},
		targetPositions:    map[int][]domain.PositionSample{},
		lastSampleSec:      map[int]float64{},
		targetFlashes:      map[int][]domain.FlashAssistEvent{},
		targetUtilByRound:  map[int]int{},
		targetZoneTime:     map[int]map[string]float64{},
		lastZoneSampleSec:  map[int]float64{},
		lastZone:           map[int]string{},
		opponentDeathZones: map[int][]string{},
		opponentBombSites:  map[int]string{},
		timelines:          map[int]*roundTimelineState{},
		recentKillTimes:    map[uint64][]float64{},
	}

	registerHandlers(parser, state)

	if err := parser.ParseToEnd(); err != nil {
		return domain.MatchStats{}, fmt.Errorf("parse: %w", err)
	}

	return state.finalize(parser)
}

type parseState struct {
	targetName        string
	targetID          uint64
	mapName           string
	players           map[uint64]*domain.PlayerStats
	damage            map[uint64]int
	kastRound         map[int]map[uint64]bool
	roundIdx          int
	rounds            []domain.RoundSummary
	curRound          *domain.RoundSummary
	matchStart        bool
	aliveTargetTeam   map[int]int
	aliveOpponentTeam map[int]int
	clutchTriggered   bool
	clutchHero        uint64
	clutchVs          int
	clutchHeroSide    string
	teamDeathOrder    map[int][]string
	teamPushZones     map[int]map[string]int

	targetGrenades     map[int][]domain.GrenadeEvent
	grenadeByEntity    map[int]*pendingGrenade
	targetPositions    map[int][]domain.PositionSample
	lastSampleSec      map[int]float64
	targetFlashes      map[int][]domain.FlashAssistEvent
	targetUtilByRound  map[int]int
	targetZoneTime     map[int]map[string]float64
	lastZoneSampleSec  map[int]float64
	lastZone           map[int]string
	opponentDeathZones map[int][]string
	opponentBombSites  map[int]string

	timelines       map[int]*roundTimelineState
	recentKillTimes map[uint64][]float64
	recentDeaths    map[int][]deathRec
	smokesByPlayer  map[uint64][]smokeLanding
}

type deathRec struct {
	steamID uint64
	team    common.Team
	timeSec float64
	zone    string
}

type smokeLanding struct {
	round   int
	timeSec float64
	x, y    float64
	zone    string
}

type pendingGrenade struct {
	roundIdx int
	idx      int
	thrower  uint64
}

func (s *parseState) playerStats(p *common.Player) *domain.PlayerStats {
	if p == nil {
		return nil
	}
	id := p.SteamID64
	if id == 0 {
		return nil
	}
	ps, ok := s.players[id]
	if !ok {
		ps = &domain.PlayerStats{
			Name:         p.Name,
			SteamID:      id,
			WeaponKills:  map[string]int{},
			GrenadeUsage: map[string]int{},
		}
		s.players[id] = ps
		if s.targetID == 0 && s.targetName != "" && strings.EqualFold(p.Name, s.targetName) {
			s.targetID = id
		}
	}
	if p.Team == common.TeamCounterTerrorists {
		ps.Team = "CT"
	} else if p.Team == common.TeamTerrorists {
		ps.Team = "T"
	}
	return ps
}

func (s *parseState) markKAST(roundIdx int, id uint64) {
	if s.kastRound[roundIdx] == nil {
		s.kastRound[roundIdx] = map[uint64]bool{}
	}
	s.kastRound[roundIdx][id] = true
}

func (s *parseState) zoneAt(p demoinfocs.Parser, x, y float64) string {
	return callouts.Normalize(s.mapName, mapZone(s.mapName, x, y))
}

func registerHandlers(p demoinfocs.Parser, s *parseState) {
	p.RegisterNetMessageHandler(func(m *msg.CSVCMsg_ServerInfo) {
		if name := m.GetMapName(); name != "" {
			s.mapName = name
		}
	})

	p.RegisterEventHandler(func(e events.MatchStart) {
		s.matchStart = true
	})

	p.RegisterEventHandler(func(e events.RoundFreezetimeEnd) {
		if s.curRound == nil {
			return
		}
		gs := p.GameState()
		targetTeam, oppTeam := resolveTeams(gs, s.targetID)
		if targetTeam != nil {
			s.curRound.TargetTeamEquipValue = teamEquipValue(targetTeam)
			s.curRound.TargetTeamEcon = econType(targetTeam, s.roundIdx)
			if tp, ok := s.players[s.targetID]; ok && tp != nil {
				_ = tp
			}
			for _, pl := range targetTeam.Members() {
				if pl != nil && pl.SteamID64 == s.targetID {
					s.curRound.TargetStartMoney = pl.Money()
					break
				}
			}
		}
		if oppTeam != nil {
			s.curRound.OpponentTeamEquipValue = teamEquipValue(oppTeam)
			s.curRound.OpponentTeamEcon = econType(oppTeam, s.roundIdx)
		}
		s.aliveTargetTeam[s.roundIdx] = countAlive(targetTeam)
		s.aliveOpponentTeam[s.roundIdx] = countAlive(oppTeam)
	})

	p.RegisterEventHandler(func(e events.RoundStart) {
		s.roundIdx++
		s.curRound = &domain.RoundSummary{Number: s.roundIdx, StartTimeSec: currentTimeSec(p)}
		s.clutchTriggered = false
		s.clutchHero = 0
		s.clutchVs = 0
		s.clutchHeroSide = ""
		tl := s.ensureTimeline(s.roundIdx)
		tl.startSec = s.curRound.StartTimeSec
	})

	p.RegisterEventHandler(func(e events.BombPlanted) {
		if s.curRound != nil {
			s.curRound.BombPlanted = true
			zone := ""
			if e.Player != nil {
				pos := e.Player.Position()
				zone = s.zoneAt(p, pos.X, pos.Y)
				s.curRound.BombSiteZone = zone
			}
			now := currentTimeSec(p)
			tl := s.ensureTimeline(s.roundIdx)
			tl.bombPlantSec = now
			tl.bombPlanted = true
			side := ""
			if e.Player != nil {
				if e.Player.Team == common.TeamTerrorists {
					side = "T"
				} else if e.Player.Team == common.TeamCounterTerrorists {
					side = "CT"
				}
			}
			detail := "下包"
			if e.Player != nil {
				detail = e.Player.Name + " 下包"
			}
			s.recordTimelineEvent(s.roundIdx, now, "bomb_plant", detail, side, zone)
		}
	})

	p.RegisterEventHandler(func(e events.SmokeStart) {
		s.recordTargetGrenade(p, "smoke", e.Position.X, e.Position.Y, e.Thrower, e.GrenadeEntityID)
		now := currentTimeSec(p)
		zone := s.zoneAt(p, e.Position.X, e.Position.Y)
		side := ""
		thrower := ""
		if e.Thrower != nil {
			thrower = e.Thrower.Name
			if e.Thrower.Team == common.TeamTerrorists {
				side = "T"
			} else if e.Thrower.Team == common.TeamCounterTerrorists {
				side = "CT"
			}
		}
		s.recordSmokeLanding(p, e.Thrower, e.Position.X, e.Position.Y, now, zone)
		if s.curRound != nil {
			tl := s.ensureTimeline(s.roundIdx)
			if tl.executeStartSec == 0 && side != "" && tl.startSec > 0 && now-tl.startSec >= 8 {
				tl.executeStartSec = now
			}
			s.recordTimelineEvent(s.roundIdx, now, "smoke", thrower+" 烟雾→"+zone, side, zone)
		}
	})
	p.RegisterEventHandler(func(e events.HeExplode) {
		s.recordTargetGrenade(p, "he", e.Position.X, e.Position.Y, e.Thrower, e.GrenadeEntityID)
	})
	p.RegisterEventHandler(func(e events.FlashExplode) {
		s.recordTargetGrenade(p, "flash", e.Position.X, e.Position.Y, e.Thrower, e.GrenadeEntityID)
	})
	p.RegisterEventHandler(func(e events.InfernoStart) {
		if e.Inferno == nil || e.Inferno.Entity == nil {
			return
		}
		pos := e.Inferno.Entity.Position()
		s.recordTargetGrenade(p, "molotov", pos.X, pos.Y, e.Inferno.Thrower(), int(e.Inferno.UniqueID()))
	})

	p.RegisterEventHandler(func(e events.FrameDone) {
		if s.targetID == 0 || s.curRound == nil {
			return
		}
		tp, ok := s.players[s.targetID]
		if !ok || tp == nil {
			return
		}
		var pl *common.Player
		gs := p.GameState()
		if gs == nil {
			return
		}
		for _, m := range append(gs.TeamCounterTerrorists().Members(), gs.TeamTerrorists().Members()...) {
			if m != nil && m.SteamID64 == s.targetID {
				pl = m
				break
			}
		}
		if pl == nil || !pl.IsAlive() {
			s.lastZone[s.roundIdx] = ""
			return
		}
		now := currentTimeSec(p)
		pos := pl.Position()
		zone := s.zoneAt(p, pos.X, pos.Y)

		// 5s 一次稀采样存到 PositionTrack（喂给 LLM）
		if now-s.lastSampleSec[s.roundIdx] >= 5.0 {
			if zone != "" {
				s.targetPositions[s.roundIdx] = append(s.targetPositions[s.roundIdx],
					domain.PositionSample{TimeSec: round2(now - s.curRound.StartTimeSec), Zone: zone})
			}
			s.lastSampleSec[s.roundIdx] = now
		}

		// 每帧累计 zone 占据时间（地图控制权重）
		last := s.lastZoneSampleSec[s.roundIdx]
		if last > 0 && zone != "" {
			delta := now - last
			if delta > 0 && delta < 1.0 {
				if s.targetZoneTime[s.roundIdx] == nil {
					s.targetZoneTime[s.roundIdx] = map[string]float64{}
				}
				s.targetZoneTime[s.roundIdx][zone] += delta
			}
		}
		s.lastZoneSampleSec[s.roundIdx] = now
		s.lastZone[s.roundIdx] = zone
	})

	p.RegisterEventHandler(func(e events.BombDefused) {
		if s.curRound != nil {
			s.curRound.BombDefused = true
			now := currentTimeSec(p)
			tl := s.ensureTimeline(s.roundIdx)
			tl.bombDefuseSec = now
			tl.bombDefused = true
			detail := "拆除"
			if e.Player != nil {
				detail = e.Player.Name + " 拆除"
			}
			s.recordTimelineEvent(s.roundIdx, now, "bomb_defuse", detail, "CT", "")
		}
	})

	p.RegisterEventHandler(func(e events.RoundEnd) {
		if s.curRound == nil {
			return
		}
		switch e.Winner {
		case common.TeamCounterTerrorists:
			s.curRound.WinnerTeam = "CT"
		case common.TeamTerrorists:
			s.curRound.WinnerTeam = "T"
		}
		s.curRound.EndReason = roundEndReasonString(e.Reason)
		s.curRound.EndTimeSec = currentTimeSec(p)
		tl := s.ensureTimeline(s.roundIdx)
		tl.endSec = s.curRound.EndTimeSec
		tl.winnerTeam = s.curRound.WinnerTeam
		tl.endReason = s.curRound.EndReason
		s.recordTimelineEvent(s.roundIdx, s.curRound.EndTimeSec, "round_end",
			fmt.Sprintf("%s胜(%s)", s.curRound.WinnerTeam, s.curRound.EndReason),
			s.curRound.WinnerTeam, "")

		// 当 target 是 CT 时，BombSiteZone 反映对手 T 的进攻包点；记录到对手时序
		if s.curRound.BombPlanted && s.curRound.BombSiteZone != "" {
			if tp, ok := s.players[s.targetID]; ok && tp != nil && tp.Team == "CT" {
				s.opponentBombSites[s.roundIdx] = s.curRound.BombSiteZone
			}
		}

		if s.clutchTriggered && s.clutchHero != 0 {
			situ := fmt.Sprintf("1v%d", s.clutchVs)
			s.curRound.ClutchSituation = situ
			heroWon := s.curRound.WinnerTeam == s.clutchHeroSide
			if s.clutchHero == s.targetID {
				if heroWon {
					s.curRound.ClutchOutcome = "你赢下" + situ
				} else {
					s.curRound.ClutchOutcome = "你输掉" + situ
				}
			} else {
				heroName := ""
				if hp, ok := s.players[s.clutchHero]; ok {
					heroName = hp.Name
				}
				if heroWon {
					s.curRound.ClutchOutcome = heroName + "赢下" + situ
				} else {
					s.curRound.ClutchOutcome = heroName + "输掉" + situ
				}
			}
		}

		s.rounds = append(s.rounds, *s.curRound)
		s.curRound = nil
	})

	p.RegisterEventHandler(func(e events.Kill) {
		if e.Killer == nil {
			return
		}
		killer := s.playerStats(e.Killer)
		if killer == nil {
			return
		}
		killer.Kills++
		s.markKAST(s.roundIdx, e.Killer.SteamID64)

		wep := "unknown"
		if e.Weapon != nil {
			wep = e.Weapon.String()
		}
		killer.WeaponKills[wep]++
		if e.IsHeadshot {
			killer.HeadshotKills++
		}

		killTime := currentTimeSec(p)

		// 移动开枪检测：CS2 步枪精度阈值约 30 u/s，>40 视作"跑动开枪"
		killerSpeed := 0.0
		if e.Killer != nil {
			killerSpeed = playerSpeed(e.Killer)
		}
		whileMoving := killerSpeed > 40

		// trade kill 检测：受害者 5s 内倒下后这一杀是否在 5s 窗口内
		isTrade := false
		tradeGap := 0.0
		if e.Victim != nil {
			if last := s.lastDeathOfTeammate(e.Victim, killTime); last > 0 {
				gap := killTime - last
				if gap >= 0 && gap <= 5.0 {
					isTrade = true
					tradeGap = round2(gap)
				}
			}
		}

		if e.Victim != nil {
			vs := s.playerStats(e.Victim)
			if vs != nil {
				vs.Deaths++
			}
			if s.curRound != nil && e.Killer.SteamID64 == s.targetID {
				s.curRound.TargetKills++
			}
			s.markTeamDeath(e.Victim, killTime)
		}

		if e.Assister != nil {
			as := s.playerStats(e.Assister)
			if as != nil {
				as.Assists++
				s.markKAST(s.roundIdx, e.Assister.SteamID64)
			}
		}

		ke := domain.KillEvent{
			Round:        s.roundIdx,
			TimeSec:      killTime,
			Weapon:       wep,
			Headshot:     e.IsHeadshot,
			ThroughSmoke: e.ThroughSmoke,
			NoScope:      e.NoScope,
			Distance:     killDistance(e.Killer, e.Victim),
			IsTrade:      isTrade,
			TradeGapSec:  tradeGap,
			WhileMoving:  whileMoving,
			KillerSpeed:  round2(killerSpeed),
		}
		if e.Killer != nil {
			pos := e.Killer.Position()
			ke.KillerZone = s.zoneAt(p, pos.X, pos.Y)
		}
		if e.Victim != nil {
			ke.Victim = e.Victim.Name
			pos := e.Victim.Position()
			ke.VictimZone = s.zoneAt(p, pos.X, pos.Y)
		}

		if s.curRound != nil && e.Victim != nil {
			vsTeamMatchesTarget := false
			if tp, ok := s.players[s.targetID]; ok && tp != nil {
				if e.Victim.SteamID64 == s.targetID {
					vsTeamMatchesTarget = true
				} else {
					victimTeam := ""
					if e.Victim.Team == common.TeamCounterTerrorists {
						victimTeam = "CT"
					} else if e.Victim.Team == common.TeamTerrorists {
						victimTeam = "T"
					}
					if victimTeam != "" && victimTeam == tp.Team {
						vsTeamMatchesTarget = true
					}
				}
			}
			if vsTeamMatchesTarget {
				s.teamDeathOrder[s.roundIdx] = append(s.teamDeathOrder[s.roundIdx], e.Victim.Name+"@"+ke.VictimZone)
				if s.teamPushZones[s.roundIdx] == nil {
					s.teamPushZones[s.roundIdx] = map[string]int{}
				}
				if ke.VictimZone != "" {
					s.teamPushZones[s.roundIdx][ke.VictimZone]++
				}
			}
		}

		if e.Victim != nil && e.Victim.SteamID64 == s.targetID && s.curRound != nil {
			s.curRound.TargetDied = true
			s.curRound.TargetDeathTimeSec = killTime
			s.curRound.TargetDeathZone = ke.VictimZone
		}

		// 对手阵亡——给"对手战术预测"维度积累死位时序
		if e.Victim != nil && s.curRound != nil && ke.VictimZone != "" {
			if tp, ok := s.players[s.targetID]; ok && tp != nil {
				victimTeam := ""
				if e.Victim.Team == common.TeamCounterTerrorists {
					victimTeam = "CT"
				} else if e.Victim.Team == common.TeamTerrorists {
					victimTeam = "T"
				}
				if tp.Team != "" && victimTeam != "" && victimTeam != tp.Team {
					s.opponentDeathZones[s.roundIdx] = append(s.opponentDeathZones[s.roundIdx], ke.VictimZone)
				}
			}
		}

		if s.curRound != nil && s.curRound.FirstKill == nil {
			fk := ke
			fk.Victim = ""
			if e.Victim != nil {
				fk.Victim = e.Killer.Name + "→" + e.Victim.Name
			}
			s.curRound.FirstKill = &fk
			s.curRound.FirstContactSec = killTime - s.curRound.StartTimeSec
			tl := s.ensureTimeline(s.roundIdx)
			tl.firstContactSec = killTime
		}

		// timeline 事件 + 连杀间隔记录
		killerSide := teamSide(e.Killer)
		killerName := ""
		victimName := ""
		if e.Killer != nil {
			killerName = e.Killer.Name
		}
		if e.Victim != nil {
			victimName = e.Victim.Name
		}
		detail := killerName + "→" + victimName + "(" + wep + ")"
		if e.IsHeadshot {
			detail += "[HS]"
		}
		if isTrade {
			detail += fmt.Sprintf("[trade %.1fs]", tradeGap)
		}
		s.recordTimelineEvent(s.roundIdx, killTime, "kill", detail, killerSide, ke.KillerZone)
		s.recentKillTimes[e.Killer.SteamID64] = append(s.recentKillTimes[e.Killer.SteamID64], killTime)

		if e.Killer.SteamID64 == s.targetID {
			killer.Highlights = append(killer.Highlights, ke)
			if s.curRound != nil {
				s.curRound.TargetEvents = append(s.curRound.TargetEvents, ke)
			}
		}

		gs := p.GameState()
		targetTeam, oppTeam := resolveTeams(gs, s.targetID)
		myAlive := countAlive(targetTeam)
		oppAlive := countAlive(oppTeam)
		if !s.clutchTriggered && targetTeam != nil && oppTeam != nil {
			if myAlive == 1 && oppAlive >= 1 {
				hero := findLastAlive(targetTeam)
				if hero != nil {
					s.clutchTriggered = true
					s.clutchHero = hero.SteamID64
					s.clutchVs = oppAlive
					switch targetTeam.Team() {
					case common.TeamCounterTerrorists:
						s.clutchHeroSide = "CT"
					case common.TeamTerrorists:
						s.clutchHeroSide = "T"
					}
					tl := s.ensureTimeline(s.roundIdx)
					tl.clutchStartSec = killTime
					s.recordTimelineEvent(s.roundIdx, killTime, "clutch_start",
						fmt.Sprintf("%s 1v%d 残局触发", hero.Name, oppAlive), s.clutchHeroSide, "")
				}
			} else if oppAlive == 1 && myAlive >= 1 {
				hero := findLastAlive(oppTeam)
				if hero != nil {
					s.clutchTriggered = true
					s.clutchHero = hero.SteamID64
					s.clutchVs = myAlive
					switch oppTeam.Team() {
					case common.TeamCounterTerrorists:
						s.clutchHeroSide = "CT"
					case common.TeamTerrorists:
						s.clutchHeroSide = "T"
					}
					tl := s.ensureTimeline(s.roundIdx)
					tl.clutchStartSec = killTime
					s.recordTimelineEvent(s.roundIdx, killTime, "clutch_start",
						fmt.Sprintf("%s 1v%d 残局触发", hero.Name, myAlive), s.clutchHeroSide, "")
				}
			}
		}
	})

	p.RegisterEventHandler(func(e events.PlayerHurt) {
		if e.Attacker == nil {
			return
		}
		atk := s.playerStats(e.Attacker)
		if atk == nil {
			return
		}
		dmg := e.HealthDamage
		if e.Player != nil && e.Player.Health() <= 0 {
			dmg += e.Player.Health()
		}
		if dmg < 0 {
			dmg = e.HealthDamage
		}
		s.damage[e.Attacker.SteamID64] += dmg

		isUtil := false
		utilType := ""
		if e.Weapon != nil {
			if e.Weapon.Class() == common.EqClassGrenade {
				atk.UtilityDamage += dmg
				isUtil = true
				utilType = grenadeKindFromWeapon(e.Weapon.String())
			}
		}
		if isUtil && e.Attacker.SteamID64 == s.targetID && s.curRound != nil {
			s.targetUtilByRound[s.roundIdx] += dmg
			gs := s.targetGrenades[s.roundIdx]
			for i := len(gs) - 1; i >= 0; i-- {
				if utilType != "" && gs[i].Type != utilType {
					continue
				}
				gs[i].DamageDealt += dmg
				break
			}
			s.targetGrenades[s.roundIdx] = gs
		}
	})

	p.RegisterEventHandler(func(e events.WeaponFire) {
		if e.Shooter == nil || e.Weapon == nil {
			return
		}
		if e.Weapon.Class() == common.EqClassGrenade {
			ps := s.playerStats(e.Shooter)
			if ps != nil {
				ps.GrenadeUsage[e.Weapon.String()]++
			}
		}
	})

	p.RegisterEventHandler(func(e events.PlayerFlashed) {
		if e.Attacker == nil || e.Player == nil {
			return
		}
		if e.Attacker.Team != e.Player.Team {
			atk := s.playerStats(e.Attacker)
			if atk != nil {
				atk.FlashAssists++
			}
		}
		if e.Attacker.SteamID64 != s.targetID || s.curRound == nil {
			return
		}
		dur := e.FlashDuration().Seconds()
		// 同队闪盲只统计不降级，敌方闪盲算辅助
		isEnemy := e.Attacker.Team != e.Player.Team
		zone := ""
		if e.Player != nil {
			pos := e.Player.Position()
			zone = s.zoneAt(p, pos.X, pos.Y)
		}
		fa := domain.FlashAssistEvent{
			Round:      s.roundIdx,
			TimeSec:    round2(currentTimeSec(p) - s.curRound.StartTimeSec),
			VictimName: e.Player.Name,
			VictimZone: zone,
		}
		s.targetFlashes[s.roundIdx] = append(s.targetFlashes[s.roundIdx], fa)

		// 找最近一颗 target 投出的 flash，计入受闪人数
		gs := s.targetGrenades[s.roundIdx]
		for i := len(gs) - 1; i >= 0; i-- {
			if gs[i].Type == "flash" {
				if isEnemy {
					gs[i].EnemiesFlashed++
				} else {
					gs[i].TeamFlashed++
				}
				if dur > gs[i].FlashDuration {
					gs[i].FlashDuration = round2(dur)
				}
				break
			}
		}
		s.targetGrenades[s.roundIdx] = gs
	})
}

func currentTimeSec(p demoinfocs.Parser) float64 {
	tr := p.TickRate()
	if tr <= 0 {
		return 0
	}
	return float64(p.GameState().IngameTick()) / tr
}

func (s *parseState) recordTargetGrenade(p demoinfocs.Parser, kind string, x, y float64, thrower *common.Player, entityID int) {
	if s.curRound == nil || thrower == nil || s.targetID == 0 {
		return
	}
	if thrower.SteamID64 != s.targetID {
		return
	}
	now := currentTimeSec(p)
	g := domain.GrenadeEvent{
		Type:        kind,
		ThrownAtSec: round2(now - s.curRound.StartTimeSec),
		LandingZone: s.zoneAt(p, x, y),
	}
	tpos := thrower.Position()
	g.ThrowerZone = s.zoneAt(p, tpos.X, tpos.Y)
	s.targetGrenades[s.roundIdx] = append(s.targetGrenades[s.roundIdx], g)
	if entityID != 0 {
		s.grenadeByEntity[entityID] = &pendingGrenade{
			roundIdx: s.roundIdx,
			idx:      len(s.targetGrenades[s.roundIdx]) - 1,
			thrower:  thrower.SteamID64,
		}
	}
}

func grenadeKindFromWeapon(w string) string {
	w = strings.ToLower(w)
	switch {
	case strings.Contains(w, "smoke"):
		return "smoke"
	case strings.Contains(w, "flash"):
		return "flash"
	case strings.Contains(w, "molot") || strings.Contains(w, "incend"):
		return "molotov"
	case strings.Contains(w, "he") || strings.Contains(w, "grenade"):
		return "he"
	}
	return ""
}

func killDistance(a, b *common.Player) float64 {
	if a == nil || b == nil {
		return 0
	}
	pa := a.Position()
	pb := b.Position()
	dx := pa.X - pb.X
	dy := pa.Y - pb.Y
	dz := pa.Z - pb.Z
	return math.Sqrt(dx*dx+dy*dy+dz*dz) / 100.0
}

func playerSpeed(pl *common.Player) float64 {
	if pl == nil {
		return 0
	}
	pawn := pl.PlayerPawnEntity()
	if pawn == nil {
		return 0
	}
	val, ok := pawn.PropertyValue("m_vecVelocity")
	if !ok {
		return 0
	}
	v := val.R3Vec()
	return math.Sqrt(v.X*v.X + v.Y*v.Y)
}

func teamSide(pl *common.Player) string {
	if pl == nil {
		return ""
	}
	switch pl.Team {
	case common.TeamTerrorists:
		return "T"
	case common.TeamCounterTerrorists:
		return "CT"
	}
	return ""
}

// lastDeathOfTeammate 找到 victim 队友里最近一次死亡时间，用于 trade kill 判定。
func (s *parseState) lastDeathOfTeammate(victim *common.Player, killTime float64) float64 {
	if victim == nil {
		return 0
	}
	team := victim.Team
	last := 0.0
	for _, ds := range s.recentDeaths[s.roundIdx] {
		if ds.team == team && ds.timeSec <= killTime && ds.timeSec > last {
			last = ds.timeSec
		}
	}
	return last
}

func (s *parseState) markTeamDeath(victim *common.Player, killTime float64) {
	if victim == nil {
		return
	}
	if s.recentDeaths == nil {
		s.recentDeaths = map[int][]deathRec{}
	}
	s.recentDeaths[s.roundIdx] = append(s.recentDeaths[s.roundIdx], deathRec{
		steamID: victim.SteamID64,
		team:    victim.Team,
		timeSec: killTime,
		zone:    "",
	})
}

// recordSmokeLanding 在 SmokeStart 时记录烟雾落点+投掷者，给 SmokeReport 用。
func (s *parseState) recordSmokeLanding(p demoinfocs.Parser, thrower *common.Player, x, y, t float64, zone string) {
	if thrower == nil {
		return
	}
	if s.smokesByPlayer == nil {
		s.smokesByPlayer = map[uint64][]smokeLanding{}
	}
	s.smokesByPlayer[thrower.SteamID64] = append(s.smokesByPlayer[thrower.SteamID64], smokeLanding{
		round:   s.roundIdx,
		timeSec: t,
		x:       x,
		y:       y,
		zone:    zone,
	})
}

func (s *parseState) finalize(p demoinfocs.Parser) (domain.MatchStats, error) {
	totalRounds := len(s.rounds)

	if s.targetID == 0 && s.targetName != "" {
		for id, ps := range s.players {
			if strings.Contains(strings.ToLower(ps.Name), s.targetName) {
				s.targetID = id
				break
			}
		}
	}

	for id, ps := range s.players {
		if ps.Kills > 0 {
			ps.HeadshotPct = round2(float64(ps.HeadshotKills) / float64(ps.Kills) * 100)
		}
		if totalRounds > 0 {
			ps.ADR = round2(float64(s.damage[id]) / float64(totalRounds))
			kastRounds := 0
			for r := 1; r <= totalRounds; r++ {
				if s.kastRound[r][id] {
					kastRounds++
				}
			}
			ps.KAST = round2(float64(kastRounds) / float64(totalRounds) * 100)
		}
	}

	stats := domain.MatchStats{
		Map:         s.mapName,
		DurationSec: int(p.CurrentTime().Seconds()),
		TickRate:    p.TickRate(),
		RoundsTotal: totalRounds,
		Rounds:      s.rounds,
	}

	gs := p.GameState()
	stats.ScoreT = gs.TeamTerrorists().Score()
	stats.ScoreCT = gs.TeamCounterTerrorists().Score()

	allNames := make([]string, 0, len(s.players))
	for _, ps := range s.players {
		if ps.Name != "" {
			allNames = append(allNames, ps.Name)
		}
	}
	sort.Strings(allNames)
	stats.AllPlayers = allNames

	if s.targetID == 0 {
		return stats, &TargetNotFoundError{Hint: s.targetName, Candidates: allNames}
	}
	if ps, ok := s.players[s.targetID]; ok {
		stats.Target = *ps
	}

	for _, ps := range s.players {
		if ps.SteamID == stats.Target.SteamID {
			continue
		}
		switch {
		case ps.Team == stats.Target.Team && stats.Target.Team != "":
			stats.Teammates = append(stats.Teammates, *ps)
		case ps.Team != "" && stats.Target.Team != "" && ps.Team != stats.Target.Team:
			stats.Opponents = append(stats.Opponents, *ps)
		}
	}

	for i := range stats.Rounds {
		r := &stats.Rounds[i]
		if order := s.teamDeathOrder[r.Number]; len(order) > 0 {
			r.TeamFirstDeath = order[0]
			if len(order) >= 4 {
				r.TeamLastAlive = "倒数第二: " + order[len(order)-2]
			}
		}
		if zones := s.teamPushZones[r.Number]; len(zones) > 0 {
			type zc struct {
				z string
				c int
			}
			arr := make([]zc, 0, len(zones))
			for z, c := range zones {
				arr = append(arr, zc{z, c})
			}
			sort.Slice(arr, func(i, j int) bool { return arr[i].c > arr[j].c })
			out := make([]string, 0, 3)
			for _, x := range arr {
				out = append(out, fmt.Sprintf("%s×%d", x.z, x.c))
				if len(out) >= 3 {
					break
				}
			}
			r.TeamPushZones = out
		}

		ta := buildTargetActions(s, r)
		if ta != nil {
			r.TargetActions = ta
		}
		oc := buildOpponentContext(s, stats.Rounds, i)
		if oc != nil {
			r.OpponentContext = oc
		}
	}

	s.finalizeTimelines(stats.Rounds)

	stats.Target.AimQuality = computeAimQuality(stats.Target, stats.Rounds, s.recentKillTimes[s.targetID])
	stats.Target.Pressure = computePressureSplit(stats.Target, stats.Rounds, s.targetID, stats.RoundsTotal)
	stats.TeamSync = computeTeamSync(stats, s)
	stats.SmokeReport = computeSmokeReport(stats.Map, stats.Target, s.smokesByPlayer[s.targetID])
	stats.Movement = computeMovementDiscipline(stats.Target, stats.Rounds)

	return stats, nil
}

func buildTargetActions(s *parseState, r *domain.RoundSummary) *domain.TargetRoundActions {
	grenades := s.targetGrenades[r.Number]
	positions := s.targetPositions[r.Number]
	flashes := s.targetFlashes[r.Number]
	zoneTime := s.targetZoneTime[r.Number]
	utilDmg := s.targetUtilByRound[r.Number]
	if len(grenades) == 0 && len(positions) == 0 && len(flashes) == 0 && len(zoneTime) == 0 && utilDmg == 0 {
		return nil
	}
	occ := map[string]float64{}
	totalTime := 0.0
	for _, t := range zoneTime {
		totalTime += t
	}
	if totalTime > 0 {
		for z, t := range zoneTime {
			occ[z] = round2(t / totalTime * 100)
		}
	}
	score := computeControlScore(zoneTime, totalTime, len(grenades), utilDmg)
	return &domain.TargetRoundActions{
		Grenades:      grenades,
		PositionTrack: positions,
		FlashAssists:  flashes,
		UtilityDamage: utilDmg,
		ZoneOccupancy: occ,
		ControlScore:  score,
	}
}

func computeControlScore(zoneTime map[string]float64, total float64, grenadeCount, utilDmg int) int {
	score := 0
	if total > 0 {
		// 单一 zone 占比 < 60% 加分（说明在动）
		maxRatio := 0.0
		for _, t := range zoneTime {
			if r := t / total; r > maxRatio {
				maxRatio = r
			}
		}
		switch {
		case maxRatio < 0.4:
			score += 30
		case maxRatio < 0.6:
			score += 20
		default:
			score += 5
		}
	}
	score += grenadeCount * 8
	score += utilDmg / 5
	if score > 100 {
		score = 100
	}
	return score
}

func buildOpponentContext(s *parseState, rounds []domain.RoundSummary, idx int) *domain.OpponentContext {
	if idx == 0 {
		return nil
	}
	bombs := []string{}
	deaths := []string{}
	econs := []string{}
	for j := idx - 3; j < idx; j++ {
		if j < 0 {
			continue
		}
		rn := rounds[j].Number
		if site := s.opponentBombSites[rn]; site != "" {
			bombs = append(bombs, fmt.Sprintf("R%d=%s", rn, site))
		}
		if zs := s.opponentDeathZones[rn]; len(zs) > 0 {
			zones := topZones(zs, 2)
			deaths = append(deaths, fmt.Sprintf("R%d:%s", rn, strings.Join(zones, "/")))
		}
		if e := rounds[j].OpponentTeamEcon; e != "" {
			econs = append(econs, fmt.Sprintf("R%d=%s", rn, e))
		}
	}
	if len(bombs) == 0 && len(deaths) == 0 && len(econs) == 0 {
		return nil
	}
	intent, evidence := predictOpponentIntent(bombs, deaths, econs, rounds, idx)
	return &domain.OpponentContext{
		RecentBombSites:    bombs,
		RecentDeathZones:   deaths,
		RecentEcons:        econs,
		PredictedIntent:    intent,
		PredictionEvidence: evidence,
	}
}

func topZones(zs []string, n int) []string {
	cnt := map[string]int{}
	for _, z := range zs {
		cnt[z]++
	}
	type zc struct {
		z string
		c int
	}
	arr := make([]zc, 0, len(cnt))
	for z, c := range cnt {
		arr = append(arr, zc{z, c})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].c > arr[j].c })
	out := make([]string, 0, n)
	for i, x := range arr {
		if i >= n {
			break
		}
		out = append(out, x.z)
	}
	return out
}

func predictOpponentIntent(bombs, deaths, econs []string, rounds []domain.RoundSummary, idx int) (string, string) {
	aCount, bCount := 0, 0
	for _, b := range bombs {
		bl := strings.ToLower(b)
		if strings.Contains(bl, "a") && !strings.Contains(bl, "b") {
			aCount++
		}
		if strings.Contains(bl, "b") {
			bCount++
		}
	}
	for _, d := range deaths {
		if strings.Contains(d, "A") {
			aCount++
		}
		if strings.Contains(d, "B") {
			bCount++
		}
	}
	curEcon := ""
	if idx < len(rounds) {
		curEcon = rounds[idx].OpponentTeamEcon
	}
	intent := ""
	switch {
	case aCount >= bCount+2:
		intent = "对手最近重点压 A 包/A 区域，本回合倾向继续打 A 或假 A 真 B"
	case bCount >= aCount+2:
		intent = "对手最近重点压 B 包/B 区域，本回合倾向继续打 B 或假 B 真 A"
	case aCount > 0 || bCount > 0:
		intent = "对手攻防点位分散，本回合可能根据经济与首接触动态选边"
	default:
		intent = "近 3 回合数据不足以稳定推测，建议根据首接触位置实时判断"
	}
	if curEcon == "eco" {
		intent += "；本回合对手为经济局，进攻欲望低，提防抢枪/单点 rush"
	} else if curEcon == "force" {
		intent += "；本回合对手强起，倾向集中一路 rush 抢回合"
	} else if curEcon == "full" {
		intent += "；本回合对手满装，会执行完整战术，看默认控图"
	}
	evidence := ""
	if len(bombs) > 0 {
		evidence += "近 3 回合包点：" + strings.Join(bombs, ",") + "；"
	}
	if len(deaths) > 0 {
		evidence += "近 3 回合对手死位：" + strings.Join(deaths, ",") + "；"
	}
	if len(econs) > 0 {
		evidence += "近 3 回合对手经济：" + strings.Join(econs, ",")
	}
	return intent, evidence
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func roundEndReasonString(r events.RoundEndReason) string {
	switch r {
	case events.RoundEndReasonTargetBombed:
		return "BombDetonated"
	case events.RoundEndReasonBombDefused:
		return "BombDefused"
	case events.RoundEndReasonCTWin:
		return "CTWin"
	case events.RoundEndReasonTerroristsWin:
		return "TerroristsWin"
	case events.RoundEndReasonTargetSaved:
		return "TargetSaved"
	case events.RoundEndReasonTerroristsSurrender:
		return "TerroristsSurrender"
	case events.RoundEndReasonCTSurrender:
		return "CTSurrender"
	case events.RoundEndReasonTerroristsPlanted:
		return "TerroristsPlanted"
	case events.RoundEndReasonCTsReachedHostage:
		return "CTsReachedHostage"
	}
	return fmt.Sprintf("Reason(%d)", r)
}

func resolveTeams(gs demoinfocs.GameState, targetID uint64) (target, opp *common.TeamState) {
	if gs == nil {
		return nil, nil
	}
	t := gs.TeamTerrorists()
	ct := gs.TeamCounterTerrorists()
	for _, pl := range t.Members() {
		if pl != nil && pl.SteamID64 == targetID {
			return t, ct
		}
	}
	for _, pl := range ct.Members() {
		if pl != nil && pl.SteamID64 == targetID {
			return ct, t
		}
	}
	return nil, nil
}

func teamEquipValue(t *common.TeamState) int {
	if t == nil {
		return 0
	}
	v := 0
	for _, pl := range t.Members() {
		if pl == nil {
			continue
		}
		v += pl.EquipmentValueCurrent()
	}
	return v
}

func econType(t *common.TeamState, roundIdx int) string {
	if t == nil {
		return ""
	}
	if roundIdx == 1 || roundIdx == 13 {
		return "pistol"
	}
	v := teamEquipValue(t)
	switch {
	case v < 5000:
		return "eco"
	case v < 16000:
		return "force"
	case v < 22000:
		return "semi"
	default:
		return "full"
	}
}

func countAlive(t *common.TeamState) int {
	if t == nil {
		return 0
	}
	n := 0
	for _, pl := range t.Members() {
		if pl != nil && pl.IsAlive() {
			n++
		}
	}
	return n
}

func findLastAlive(t *common.TeamState) *common.Player {
	if t == nil {
		return nil
	}
	for _, pl := range t.Members() {
		if pl != nil && pl.IsAlive() {
			return pl
		}
	}
	return nil
}
