package structural

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

const maxConfirmBytes = 2_000_000

func rows(it model.ContextItem) []map[string]any {
	payload := map[string]any{}
	if json.Unmarshal([]byte(it.Text), &payload) != nil {
		return nil
	}
	raw, ok := payload["results"].([]any)
	if !ok {
		raw, ok = payload["impacted_nodes"].([]any)
	}
	if !ok {
		return nil
	}
	out := []map[string]any{}
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func rowPath(row map[string]any) string {
	for _, k := range []string{"file_path", "relative_path", "path"} {
		if v := strings.ReplaceAll(strings.TrimSpace(str(row[k])), `\`, "/"); v != "" {
			return v
		}
	}
	return ""
}

// tail ports V2's value.rsplit(".", 1)[-1].rsplit("::", 1)[-1].
func tail(v string) string {
	if i := strings.LastIndex(v, "."); i >= 0 {
		v = v[i+1:]
	}
	if i := strings.LastIndex(v, "::"); i >= 0 {
		v = v[i+2:]
	}
	return v
}

func rowNames(row map[string]any) map[string]bool {
	names := map[string]bool{}
	for _, k := range []string{"name", "qualified_name", "parent_name", "import_target"} {
		if v := strings.TrimSpace(str(row[k])); v != "" {
			names[v] = true
			if t := tail(v); t != "" {
				names[t] = true
			}
		}
	}
	return names
}

// sourceConfirms reports whether the row's file (inside root, regular, not a
// symlink, at most 2 MB) contains one of the row's names.
func sourceConfirms(root string, row map[string]any) bool {
	rel := rowPath(row)
	if rel == "" {
		return false
	}
	var p string
	if filepath.IsAbs(filepath.FromSlash(rel)) {
		_, abs, err := workspace.Within(root, filepath.FromSlash(rel))
		if err != nil {
			return false
		}
		p = abs
	} else {
		abs, ok := confined(root, rel)
		if !ok {
			return false
		}
		p = abs
	}
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxConfirmBytes {
		return false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	text := string(b)
	for n := range rowNames(row) {
		if len(n) >= 2 && strings.Contains(text, n) {
			return true
		}
	}
	return false
}

func scipKeys(items []model.ContextItem) (paths, names map[string]bool) {
	paths, names = map[string]bool{}, map[string]bool{}
	for _, it := range items {
		if p := strings.ReplaceAll(str(it.Metadata["path"]), `\`, "/"); p != "" {
			paths[strings.ToLower(p)] = true
		}
		for _, k := range []string{"symbol_name", "symbol"} {
			if v := strings.TrimSpace(str(it.Metadata[k])); v != "" {
				names[strings.ToLower(v)] = true
				names[strings.ToLower(tail(v))] = true
			}
		}
	}
	return paths, names
}

func scipConfirms(row map[string]any, paths, names map[string]bool) bool {
	p := strings.ToLower(rowPath(row))
	if p == "" || !paths[p] {
		return false
	}
	for n := range rowNames(row) {
		if names[strings.ToLower(n)] {
			return true
		}
	}
	return false
}

func withMeta(it model.ContextItem, extra map[string]any) model.ContextItem {
	meta := make(map[string]any, len(it.Metadata)+len(extra))
	for k, v := range it.Metadata {
		meta[k] = v
	}
	for k, v := range extra {
		meta[k] = v
	}
	it.Metadata = meta
	return it
}

// Validate ports V2 validate_crg_item: CRG rows confirmed by source and SCIP
// are verified, by one oracle corroborated, otherwise candidate. Confidence is
// categorical, never a probability. Non-CRG items pass through unchanged.
func Validate(ctx context.Context, ws, rel string, items []model.ContextItem, q Query) []model.ContextItem {
	root, rootErr := repoRoot(ws, rel)
	out := make([]model.ContextItem, 0, len(items))
	for _, it := range items {
		if it.Source != "code_review_graph" {
			out = append(out, it)
			continue
		}
		rs := rows(it)
		if len(rs) == 0 {
			if it.Metadata["empty_verified"] == true {
				out = append(out, withMeta(it, map[string]any{"evidence_confidence": "corroborated",
					"confidence_basis": []string{"crg_verified_empty"}, "structural_valid": true, "high_risk_eligible": false}))
			} else {
				out = append(out, withMeta(it, map[string]any{"evidence_confidence": "candidate",
					"confidence_basis": []string{}, "structural_valid": false, "high_risk_eligible": false}))
			}
			continue
		}
		var paths, names map[string]bool
		if rootErr == nil {
			sq := q
			if sq.Symbol == "" {
				sq.Symbol = str(it.Metadata["anchor"])
			}
			sq.Limit = max(q.Limit, len(rs))
			if p := str(it.Metadata["pattern"]); p != "" {
				sq.Patterns = []string{p}
			} else {
				sq.Patterns = nil
			}
			paths, names = scipKeys(ScipContext(ctx, ws, rel, sq))
		}
		src, sc, both := 0, 0, 0
		for _, row := range rs {
			s := rootErr == nil && sourceConfirms(root, row)
			c := scipConfirms(row, paths, names)
			src, sc = src+b2i(s), sc+b2i(c)
			both += b2i(s && c)
		}
		basis := []string{}
		if src > 0 {
			basis = append(basis, "source")
		}
		if sc > 0 {
			basis = append(basis, "scip")
		}
		confidence := "candidate"
		switch {
		case both > 0:
			confidence = "verified"
		case src > 0 || sc > 0:
			confidence = "corroborated"
		}
		out = append(out, withMeta(it, map[string]any{
			"evidence_confidence": confidence, "confidence_basis": basis,
			"source_confirmed_results": src, "scip_confirmed_results": sc, "verified_results": both,
			"structural_valid": confidence != "candidate", "high_risk_eligible": confidence != "candidate",
		}))
	}
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
