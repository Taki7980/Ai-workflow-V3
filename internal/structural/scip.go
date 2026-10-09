package structural

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

const scipMaxJSON = 64 << 20

// Indexer is a SCIP indexer invocation run in the repository root.
type Indexer struct {
	Language   string
	Executable string
	Args       []string
}

var skipDirs = map[string]bool{".git": true, ".ai": true, "ai-workspace": true, ".venv": true, "venv": true,
	"node_modules": true, "__pycache__": true, "dist": true, "build": true}

func hasSource(root string, suffixes ...string) bool {
	found := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		for _, s := range suffixes {
			if ext == s {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

func indexerFor(root, language string) (Indexer, bool) {
	name := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(language)), "javascript/typescript", "typescript")
	if alias, ok := map[string]string{"py": "python", "ts": "typescript", "js": "javascript", "golang": "go"}[name]; ok {
		name = alias
	}
	switch name {
	case "python":
		return Indexer{"python", "scip-python", []string{"index", ".", "--project-name", filepath.Base(root)}}, true
	case "typescript":
		return Indexer{"typescript", "scip-typescript", []string{"index"}}, true
	case "javascript":
		return Indexer{"javascript", "scip-typescript", []string{"index", "--infer-tsconfig"}}, true
	case "java":
		return Indexer{"java", "scip-java", []string{"index"}}, true
	case "go":
		return Indexer{"go", "scip-go", []string{"./..."}}, true
	}
	return Indexer{}, false
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// DetectIndexer ports V2 detect_indexer: an explicit language wins; otherwise
// exactly one language must match its marker files and sources.
func DetectIndexer(repoRoot, language string) (Indexer, bool) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return Indexer{}, false
	}
	if language != "" {
		return indexerFor(root, language)
	}
	at := func(n string) string { return filepath.Join(root, n) }
	anyFile := func(names ...string) bool {
		for _, n := range names {
			if isFile(at(n)) {
				return true
			}
		}
		return false
	}
	var found []Indexer
	add := func(lang string, ok bool) {
		if ix, known := indexerFor(root, lang); ok && known {
			found = append(found, ix)
		}
	}
	add("go", isFile(at("go.mod")) && hasSource(root, ".go"))
	add("java", (exists(at("pom.xml")) || exists(at("build.gradle")) || exists(at("build.gradle.kts")) || exists(at("gradlew"))) && hasSource(root, ".java"))
	add("typescript", isFile(at("tsconfig.json")) && hasSource(root, ".ts", ".tsx"))
	add("python", anyFile("pyproject.toml", "setup.py", "setup.cfg", "requirements.txt") && hasSource(root, ".py"))
	add("javascript", isFile(at("package.json")) && hasSource(root, ".js", ".jsx"))
	if len(found) != 1 {
		return Indexer{}, false
	}
	return found[0], true
}

var (
	identRe = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\b`)
	tailRe  = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
)

// QueryAnchor ports V2 _query_anchor: the last distinctive identifier
// (snake_case or interior capital), else the last identifier, lowercased.
func QueryAnchor(query string) string {
	ids := identRe.FindAllString(query, -1)
	if len(ids) == 0 {
		return ""
	}
	pick := ids[len(ids)-1]
	for _, id := range ids {
		if strings.Contains(id, "_") || strings.ToLower(id[1:]) != id[1:] {
			pick = id
		}
	}
	return strings.ToLower(pick)
}

func symbolTail(raw string) string {
	parts := tailRe.FindAllString(raw, -1)
	if len(parts) == 0 {
		return raw
	}
	return parts[len(parts)-1]
}

func field(row map[string]any, camel, snake string) any {
	if v, ok := row[camel]; ok {
		return v
	}
	return row[snake]
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// confined ports V2 resolve_within_root: relative, no parent traversal, inside root.
func confined(root, rel string) (string, bool) {
	if rel == "" || strings.ContainsRune(rel, 0) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) ||
		filepath.IsAbs(rel) || filepath.VolumeName(filepath.FromSlash(rel)) != "" {
		return "", false
	}
	for _, part := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return "", false
		}
	}
	_, abs, err := workspace.Within(root, rel)
	return abs, err == nil
}

func sourceLine(root, rel string, line int) string {
	p, ok := confined(root, rel)
	if !ok {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	s := strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if line < 0 || line >= len(lines) || s == "" {
		return ""
	}
	return strings.TrimSpace(lines[line])
}

// ItemsFromScipPayload ports V2 items_from_scip_payload over `scip print --json` output.
func ItemsFromScipPayload(repoRoot string, payload map[string]any, q Query) []model.ContextItem {
	docs, ok := payload["documents"].([]any)
	if !ok || q.Limit <= 0 {
		return nil
	}
	target := strings.ToLower(strings.TrimSpace(q.Symbol))
	if target == "" {
		target = QueryAnchor(q.Text)
	}
	changed := map[string]bool{}
	for _, c := range q.Changed {
		changed[strings.ReplaceAll(c, `\`, "/")] = true
	}
	wantRefs := has(q.Patterns, "references_to")
	out := []model.ContextItem{}
	for _, d := range docs {
		doc, ok := d.(map[string]any)
		if !ok {
			continue
		}
		rel := strings.ReplaceAll(strings.TrimSpace(str(field(doc, "relativePath", "relative_path"))), `\`, "/")
		if rel == "" {
			continue
		}
		if _, ok := confined(repoRoot, rel); !ok {
			continue
		}
		language := str(doc["language"])
		if language == "" {
			language = "unknown"
		}
		infos := map[string]map[string]any{}
		for _, i := range list(doc["symbols"]) {
			if info, ok := i.(map[string]any); ok && str(info["symbol"]) != "" {
				infos[str(info["symbol"])] = info
			}
		}
		occs, ok := doc["occurrences"].([]any)
		if !ok {
			continue
		}
		for _, o := range occs {
			occ, ok := o.(map[string]any)
			if !ok {
				continue
			}
			raw := strings.TrimSpace(str(occ["symbol"]))
			if raw == "" {
				continue
			}
			display := strings.TrimSpace(str(field(infos[raw], "displayName", "display_name")))
			if display == "" {
				display = symbolTail(raw)
			}
			if target != "" && !strings.Contains(strings.ToLower(display), target) && !strings.Contains(strings.ToLower(raw), target) {
				continue
			}
			roles, _ := toInt(field(occ, "symbolRoles", "symbol_roles"))
			definition := roles&1 == 1
			if wantRefs && definition {
				continue
			}
			line := 0
			if r := list(occ["range"]); len(r) > 0 {
				line, _ = toInt(r[0])
				line = max(0, line)
			}
			role, pattern, score := "reference", "references_to", 9.0
			if definition {
				role, pattern, score = "definition", "definition_of", 10.0
			} else if changed[rel] {
				score += 0.25
			}
			shown := sourceLine(repoRoot, rel, line)
			if shown == "" {
				shown = display
			}
			out = append(out, model.ContextItem{
				Source: "scip",
				Text:   rel + ":" + strconv.Itoa(line+1) + ": " + shown + " [" + role + "] " + display,
				Score:  score,
				Metadata: map[string]any{
					"path": rel, "line": line + 1, "language": language, "symbol": raw, "symbol_name": display,
					"role": role, "pattern": pattern, "structural_valid": true, "result_count": 1,
				},
			})
			if len(out) >= q.Limit {
				return out
			}
		}
	}
	return out
}

// loadScip returns the repository root and parsed index.json when the SCIP
// index is fresh. A package variable so tests can count loads.
var loadScip = func(ctx context.Context, ws, rel string) (string, map[string]any, bool) {
	st := ScipStatus(ctx, ws, rel)
	if !st.Ready {
		return "", nil, false
	}
	root, err := repoRoot(ws, rel)
	if err != nil {
		return "", nil, false
	}
	f, err := os.Open(filepath.Join(st.DataDir, "index.json"))
	if err != nil {
		return "", nil, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, scipMaxJSON+1))
	if err != nil || len(b) > scipMaxJSON {
		return "", nil, false
	}
	payload := map[string]any{}
	if json.Unmarshal(b, &payload) != nil {
		return "", nil, false
	}
	return root, payload, true
}

// ScipContext reads a fresh SCIP JSON index for one repository.
func ScipContext(ctx context.Context, ws, rel string, q Query) []model.ContextItem {
	root, payload, ok := loadScip(ctx, ws, rel)
	if !ok {
		return nil
	}
	return ItemsFromScipPayload(root, payload, q)
}
