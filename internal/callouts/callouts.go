package callouts

import (
	"fmt"
	"sort"
	"strings"
)

type Point struct {
	Name    string
	Aliases []string
}

var byMap = map[string][]Point{
	"dust2": {
		{Name: "A长", Aliases: []string{"long", "long a", "a long", "A Long", "A长"}},
		{Name: "A大坑", Aliases: []string{"pit", "a pit", "A大坑", "A大坑/A点"}},
		{Name: "A平台", Aliases: []string{"a site", "A点", "A包点", "A平台"}},
		{Name: "A小", Aliases: []string{"short", "catwalk", "A小", "A小/CT楼梯"}},
		{Name: "中路", Aliases: []string{"mid", "middle", "中路", "中路低区"}},
		{Name: "中门", Aliases: []string{"mid doors", "中门", "中门T方"}},
		{Name: "B洞", Aliases: []string{"tunnel", "tunnels", "B洞", "B隧道", "B隧道入口"}},
		{Name: "B门", Aliases: []string{"b doors", "B门", "B包点/B门"}},
		{Name: "B平台", Aliases: []string{"b site", "B点", "B包点", "B平台"}},
		{Name: "T出生点", Aliases: []string{"t spawn", "T出生", "T出生点"}},
	},
	"mirage": {
		{Name: "A包点", Aliases: []string{"a site", "A点", "A包点"}},
		{Name: "A斜坡", Aliases: []string{"ramp", "a ramp", "A坡", "A斜坡", "T出生/A长入口"}},
		{Name: "A二楼", Aliases: []string{"palace", "A二楼"}},
		{Name: "VIP", Aliases: []string{"window", "vip", "VIP"}},
		{Name: "中路", Aliases: []string{"mid", "middle", "中路", "中路/匪徒道", "A短/中路"}},
		{Name: "拱门", Aliases: []string{"connector", "con", "拱门"}},
		{Name: "B小", Aliases: []string{"short", "cat", "B小"}},
		{Name: "B公寓", Aliases: []string{"apps", "apartments", "B公寓", "B点/B公寓"}},
		{Name: "B包点", Aliases: []string{"b site", "B点", "B包点", "B底"}},
	},
	"inferno": {
		{Name: "香蕉道", Aliases: []string{"banana", "B香蕉", "香蕉道"}},
		{Name: "B包点", Aliases: []string{"b site", "B点", "B包点", "B点/B阳台"}},
		{Name: "B警家", Aliases: []string{"ct", "ct spawn", "B底/CT出生", "CT出生"}},
		{Name: "中路", Aliases: []string{"mid", "middle", "中路", "中路/T出生"}},
		{Name: "A短", Aliases: []string{"short", "A短"}},
		{Name: "A长", Aliases: []string{"long", "A长"}},
		{Name: "A包点", Aliases: []string{"a site", "A点", "A包点", "A点/A木"}},
		{Name: "A木", Aliases: []string{"apps", "apartments", "A木"}},
	},
	"nuke": {
		{Name: "外场", Aliases: []string{"outside", "外场", "外场/外平台"}},
		{Name: "车库", Aliases: []string{"garage", "车库", "外车库/T出生"}},
		{Name: "A包点", Aliases: []string{"a site", "A平台", "A平台/上层", "上层"}},
		{Name: "B包点", Aliases: []string{"b site", "B包点", "B包点/下层", "下层"}},
		{Name: "铁板", Aliases: []string{"ramp", "ramp room", "铁板"}},
	},
	"ancient": {
		{Name: "A大房", Aliases: []string{"a main", "main", "A大房", "A点/A大房"}},
		{Name: "A包点", Aliases: []string{"a site", "A点", "A包点"}},
		{Name: "A坡道", Aliases: []string{"a ramp", "ramp", "A坡", "A坡道", "A坡道/连接处"}},
		{Name: "连接处", Aliases: []string{"connector", "conn", "连接", "连接处"}},
		{Name: "中路", Aliases: []string{"mid", "middle", "中路", "中路/洞穴"}},
		{Name: "洞穴", Aliases: []string{"cave", "donut", "洞穴"}},
		{Name: "B坡", Aliases: []string{"b ramp", "B坡", "B点/B坡"}},
		{Name: "B包点", Aliases: []string{"b site", "B点", "B包点"}},
		{Name: "B底", Aliases: []string{"b main", "B底", "B底/T出生"}},
	},
	"anubis": {
		{Name: "A街", Aliases: []string{"a main", "A街", "A点/A街"}},
		{Name: "A包点", Aliases: []string{"a site", "A点", "A包点"}},
		{Name: "A连接", Aliases: []string{"connector", "A连接"}},
		{Name: "中路", Aliases: []string{"mid", "middle", "中路", "中路/广场"}},
		{Name: "广场", Aliases: []string{"street", "广场"}},
		{Name: "B水道", Aliases: []string{"canal", "water", "B水道", "B点/B水道"}},
		{Name: "B包点", Aliases: []string{"b site", "B点", "B包点"}},
	},
	"overpass": {
		{Name: "A长", Aliases: []string{"long", "A长", "A长/A连接"}},
		{Name: "A连接", Aliases: []string{"connector", "A连接"}},
		{Name: "A包点", Aliases: []string{"a site", "A点", "A包点"}},
		{Name: "中路", Aliases: []string{"mid", "middle", "中路", "中路/水池"}},
		{Name: "水池", Aliases: []string{"party", "水池"}},
		{Name: "B隧道", Aliases: []string{"monster", "tunnel", "B隧道", "B点/B隧道"}},
		{Name: "B包点", Aliases: []string{"b site", "B点", "B包点"}},
	},
	"vertigo": {
		{Name: "A坡道", Aliases: []string{"a ramp", "ramp", "A坡", "A坡道", "A点/A坡道"}},
		{Name: "A包点", Aliases: []string{"a site", "A点", "A包点"}},
		{Name: "中路", Aliases: []string{"mid", "middle", "中路", "中路/天梯"}},
		{Name: "天梯", Aliases: []string{"ladder", "天梯"}},
		{Name: "B坡道", Aliases: []string{"b ramp", "B坡", "B坡道", "B点/B坡道"}},
		{Name: "B包点", Aliases: []string{"b site", "B点", "B包点"}},
	},
}

func MapKey(mapName string) string {
	m := strings.ToLower(strings.TrimSpace(mapName))
	m = strings.TrimPrefix(m, "de_")
	return m
}

func Normalize(mapName, zone string) string {
	zone = strings.TrimSpace(zone)
	if zone == "" {
		return ""
	}
	count := ""
	if i := strings.LastIndex(zone, "×"); i > 0 {
		count = zone[i:]
		zone = strings.TrimSpace(zone[:i])
	}
	if normalized := normalizeSingle(mapName, zone); normalized != "" {
		return normalized + count
	}
	if strings.Contains(zone, "/") {
		for _, part := range strings.Split(zone, "/") {
			if normalized := Normalize(mapName, part); normalized != "" {
				return normalized + count
			}
		}
	}
	return zone + count
}

func normalizeSingle(mapName, zone string) string {
	points := byMap[MapKey(mapName)]
	lower := strings.ToLower(zone)
	for _, point := range points {
		if zone == point.Name {
			return point.Name
		}
		for _, alias := range point.Aliases {
			if strings.EqualFold(zone, alias) || strings.Contains(lower, strings.ToLower(alias)) {
				return point.Name
			}
		}
	}
	return ""
}

func NormalizeList(mapName string, zones []string) []string {
	out := make([]string, 0, len(zones))
	for _, z := range zones {
		n := Normalize(mapName, z)
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

func NormalizeText(mapName, text string) string {
	if text == "" {
		return text
	}
	points := byMap[MapKey(mapName)]
	for _, point := range points {
		for _, alias := range point.Aliases {
			if alias == "" || alias == point.Name {
				continue
			}
			text = replaceLoose(text, alias, point.Name)
		}
	}
	return text
}

func PromptReference(mapName string) string {
	points := byMap[MapKey(mapName)]
	if len(points) == 0 {
		return "当前地图没有内置点位表；只能引用数据里已经出现过的位置名。"
	}
	names := make([]string, 0, len(points))
	for _, p := range points {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return fmt.Sprintf("本图允许点位名：%s。英文/混合叫法必须转换为这些中文名；不要创造新点位名。", strings.Join(names, "、"))
}

func replaceLoose(s, from, to string) string {
	if from == "" || from == to || !strings.Contains(strings.ToLower(s), strings.ToLower(from)) {
		return s
	}
	return strings.ReplaceAll(s, from, to)
}
