package memory

import "strings"

// NormalizeTags cleans a comma-separated tag list: lowercased, trimmed,
// deduped, and capped so a runaway list cannot bloat the row.
func NormalizeTags(s string) string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(s, ",") {
		t := strings.ToLower(strings.TrimSpace(part))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) >= 12 {
			break
		}
	}
	return strings.Join(out, ", ")
}
