package catalog

import "strings"

// MatchEcoOption 在公开 Eco 选项中选择与可用性/FQN 段最安全的唯一候选。
// 先比较原始码，再比较标准化码；同一档内优先公共前缀更长、整体更短的候选，
// 因此返回顺序不会把短配置误配成带额外磁盘的长配置。
func MatchEcoOption(opts []map[string]interface{}, wanted string) (map[string]interface{}, string, string) {
	w := strings.ToLower(strings.TrimSpace(wanted))
	if w == "" {
		return nil, "", ""
	}
	wStd := StandardizeConfig(w)
	type candidate struct {
		opt      map[string]interface{}
		code     string
		strength int
		size     int
	}
	tierNames := [4]string{"原样相等", "原始码前缀", "标准化相等", "标准化前缀"}
	var tiers [4]*candidate
	for _, option := range opts {
		code, _ := option["planCode"].(string)
		c := strings.ToLower(strings.TrimSpace(code))
		if c == "" {
			continue
		}
		cStd := StandardizeConfig(c)
		tier, strength := -1, 0
		switch {
		case c == w:
			tier, strength = 0, len(c)
		case strings.HasPrefix(c, w+"-"):
			tier, strength = 1, len(w)
		case strings.HasPrefix(w, c+"-"):
			tier, strength = 1, len(c)
		case cStd != "" && cStd == wStd:
			tier, strength = 2, len(cStd)
		case cStd != "" && wStd != "" && strings.HasPrefix(cStd, wStd):
			tier, strength = 3, len(wStd)
		case cStd != "" && wStd != "" && strings.HasPrefix(wStd, cStd):
			tier, strength = 3, len(cStd)
		}
		if tier < 0 {
			continue
		}
		current := tiers[tier]
		if current == nil || strength > current.strength || (strength == current.strength && len(c) < current.size) {
			tiers[tier] = &candidate{opt: option, code: code, strength: strength, size: len(c)}
		}
	}
	for index, candidate := range tiers {
		if candidate != nil {
			return candidate.opt, candidate.code, tierNames[index]
		}
	}
	return nil, "", ""
}

// matchAddonsForSegment 是目录 family 的字符串适配器，保留给目录解析和旧调用方。
// 它只返回一个最安全候选，绝不把同一段扩展成多个 addon。
func matchAddonsForSegment(addons []string, segment, standardized string) []string {
	options := make([]map[string]interface{}, 0, len(addons))
	for _, addon := range addons {
		options = append(options, map[string]interface{}{"planCode": addon})
	}
	wanted := segment
	if strings.TrimSpace(wanted) == "" {
		wanted = standardized
	}
	_, code, _ := MatchEcoOption(options, wanted)
	if code == "" {
		return nil
	}
	return []string{code}
}
