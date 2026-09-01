package agenticrun

import "os"

// ExpandVariables returns a new RunConfig with ${VAR} placeholders in all string fields
// replaced by values from vars. Placeholders with no matching key are left unchanged
// (i.e. "${UNKNOWN}" stays as "${UNKNOWN}").
//
// The input cfg is never mutated, which matters because the controller reuses
// a single RunConfig across the per-cluster loop.
func ExpandVariables(cfg *RunConfig, vars map[string]string) *RunConfig {
	out := &RunConfig{
		Request: expandStr(cfg.Request, vars),
	}
	for _, s := range cfg.Skills {
		out.Skills = append(out.Skills, SkillSpec{
			Image: expandStr(s.Image, vars),
			Paths: s.Paths,
		})
	}
	for _, m := range cfg.MCPServers {
		out.MCPServers = append(out.MCPServers, MCPServerSpec{
			Name:           expandStr(m.Name, vars),
			URL:            expandStr(m.URL, vars),
			TimeoutSeconds: m.TimeoutSeconds,
		})
	}
	return out
}

func expandStr(s string, vars map[string]string) string {
	return os.Expand(s, func(key string) string {
		if v, ok := vars[key]; ok {
			return v
		}
		return "${" + key + "}"
	})
}
