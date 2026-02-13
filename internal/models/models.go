package models

import "strings"

// ModelGroup represents a model and its allowed effort levels.
type ModelGroup struct {
	Base    string
	Efforts []string
}

// AllModelGroups returns the full catalog of models with their effort variants.
func AllModelGroups() []ModelGroup {
	return []ModelGroup{
		{Base: "gpt-5", Efforts: []string{"high", "medium", "low", "minimal"}},
		{Base: "gpt-5.1", Efforts: []string{"high", "medium", "low"}},
		{Base: "gpt-5.2", Efforts: []string{"xhigh", "high", "medium", "low"}},
		{Base: "gpt-5-codex", Efforts: []string{"high", "medium", "low"}},
		{Base: "gpt-5.2-codex", Efforts: []string{"xhigh", "high", "medium", "low"}},
		{Base: "gpt-5.3-codex", Efforts: []string{"xhigh", "high", "medium", "low"}},
		{Base: "gpt-5.1-codex", Efforts: []string{"high", "medium", "low"}},
		{Base: "gpt-5.1-codex-max", Efforts: []string{"xhigh", "high", "medium", "low"}},
		{Base: "gpt-5.1-codex-mini", Efforts: nil},
		{Base: "codex-mini", Efforts: nil},
	}
}

// ModelCatalog returns all model IDs. If exposeVariants is true, also includes effort-level variants.
func ModelCatalog(exposeVariants bool) []string {
	groups := AllModelGroups()
	var ids []string
	for _, g := range groups {
		ids = append(ids, g.Base)
		if exposeVariants && len(g.Efforts) > 0 {
			for _, e := range g.Efforts {
				ids = append(ids, g.Base+"-"+e)
			}
		}
	}
	return ids
}

var modelMapping = map[string]string{
	"gpt5":                 "gpt-5",
	"gpt-5-latest":         "gpt-5",
	"gpt-5":                "gpt-5",
	"gpt-5.1":              "gpt-5.1",
	"gpt5.2":               "gpt-5.2",
	"gpt-5.2":              "gpt-5.2",
	"gpt-5.2-latest":       "gpt-5.2",
	"gpt5.2-codex":         "gpt-5.2-codex",
	"gpt-5.2-codex":        "gpt-5.2-codex",
	"gpt-5.2-codex-latest": "gpt-5.2-codex",
	"gpt5-codex":           "gpt-5-codex",
	"gpt-5-codex":          "gpt-5-codex",
	"gpt-5-codex-latest":   "gpt-5-codex",
	"gpt-5.1-codex":        "gpt-5.1-codex",
	"gpt-5.1-codex-max":    "gpt-5.1-codex-max",
	"codex":                "codex-mini-latest",
	"codex-mini":           "codex-mini-latest",
	"codex-mini-latest":    "codex-mini-latest",
	"gpt-5.1-codex-mini":   "gpt-5.1-codex-mini",
}

var effortSuffixes = []string{"minimal", "low", "medium", "high", "xhigh"}

// NormalizeModelName maps model aliases to canonical names and strips effort suffixes.
func NormalizeModelName(name, debugModel string) string {
	if debugModel != "" {
		return strings.TrimSpace(debugModel)
	}
	if name == "" {
		return "gpt-5"
	}
	base := strings.TrimSpace(strings.SplitN(name, ":", 2)[0])

	// Strip effort suffixes
	lowered := strings.ToLower(base)
	for _, sep := range []string{"-", "_"} {
		for _, effort := range effortSuffixes {
			suffix := sep + effort
			if strings.HasSuffix(lowered, suffix) {
				base = base[:len(base)-len(suffix)]
				lowered = strings.ToLower(base)
				break
			}
		}
	}

	if mapped, ok := modelMapping[base]; ok {
		return mapped
	}
	return base
}

// AllowedEfforts returns the set of valid reasoning effort levels for a model.
func AllowedEfforts(model string) map[string]bool {
	base := strings.ToLower(strings.TrimSpace(model))
	if base == "" {
		return defaultEfforts()
	}
	normalized := strings.SplitN(base, ":", 2)[0]

	if strings.HasPrefix(normalized, "gpt-5.2") {
		return map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true}
	}
	if strings.HasPrefix(normalized, "gpt-5.1-codex-max") {
		return map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true}
	}
	if strings.HasPrefix(normalized, "gpt-5.1") {
		return map[string]bool{"low": true, "medium": true, "high": true}
	}
	return defaultEfforts()
}

func defaultEfforts() map[string]bool {
	return map[string]bool{"minimal": true, "low": true, "medium": true, "high": true, "xhigh": true}
}
