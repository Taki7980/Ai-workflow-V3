// Package bench is the V2 routing-and-context benchmark: case validation,
// pattern/file/span/role/trajectory metrics, token economics, frozen
// snapshots and corpus-v2 validation/integrity. It never runs a model.
package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/procx"
)

// Case is one benchmark case kept as decoded JSON (V2 accepts loose types).
type Case map[string]any

var (
	positiveTaskTypes = []string{"code2test", "comment2context", "trace2code", "edit2ripple"}
	controlTypes      = []string{"positive", "natural_no_gold", "wrong_repo"}
	relevanceRoles    = []string{"edit_target", "supporting_context"}
	pathKeys          = []string{"file", "path", "relative_path", "file_path"}
	repositoryKeys    = []string{"repository_id", "remote_identity", "repository", "repo", "repo_id"}
	gitSHA            = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)
	sha256RE          = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	slashes           = regexp.MustCompile(`/+`)
	textPathRE        = regexp.MustCompile(`^(.+?):\d+(?::\d+)?:\s`)
	testFileRE        = regexp.MustCompile(`Detected test file:\s+(.+?)\s+\(for\s+`)
)

// Str returns c[k] as a trimmed string ("" when absent or null).
func (c Case) Str(k string) string {
	switch v := c[k].(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// Int returns c[k] as an int, accepting JSON numbers and numeric strings.
func (c Case) Int(k string, def int) (int, error) {
	switch v := c[k].(type) {
	case nil:
		return def, nil
	case json.Number:
		i, err := strconv.Atoi(v.String())
		if err != nil {
			f, ferr := v.Float64()
			if ferr != nil || f != float64(int(f)) {
				return 0, err
			}
			return int(f), nil
		}
		return i, nil
	case float64:
		return int(v), nil
	case string:
		return strconv.Atoi(strings.TrimSpace(v))
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	}
	return 0, errors.New("not an integer")
}

func (c Case) list(k string) []any {
	l, _ := c[k].([]any)
	return l
}

// Strings returns c[k] as []string when it is a list of non-blank strings.
func (c Case) Strings(k string) ([]string, bool) {
	raw, present := c[k]
	if !present || raw == nil {
		return nil, true
	}
	l, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(l))
	for _, v := range l {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// NormalizePath is V2 normalize_file_path.
func NormalizePath(v string) string {
	n := strings.ReplaceAll(strings.TrimSpace(v), `\`, "/")
	for strings.HasPrefix(n, "./") {
		n = n[2:]
	}
	return strings.Trim(slashes.ReplaceAllString(n, "/"), "/")
}

func insideRelative(v string) (string, bool) {
	n := NormalizePath(v)
	if n == "" || filepath.IsAbs(n) || strings.HasPrefix(n, "/") || slices.Contains(strings.Split(n, "/"), "..") {
		return "", false
	}
	return n, true
}

func jsonObject(text string) map[string]any {
	var m map[string]any
	if json.Unmarshal([]byte(text), &m) != nil {
		return nil
	}
	return m
}

// ItemFile returns the repository-relative file an item refers to.
func ItemFile(it model.ContextItem) string {
	for _, c := range []map[string]any{it.Metadata, it.Provenance, jsonObject(it.Text)} {
		for _, k := range pathKeys {
			if s, ok := c[k].(string); ok && strings.TrimSpace(s) != "" {
				return NormalizePath(s)
			}
		}
	}
	if m := textPathRE.FindStringSubmatch(it.Text); m != nil {
		return NormalizePath(m[1])
	}
	if m := testFileRE.FindStringSubmatch(it.Text); m != nil {
		return NormalizePath(m[1])
	}
	return ""
}

// ItemRepository returns the repository identity an item carries.
func ItemRepository(it model.ContextItem) string {
	for _, c := range []map[string]any{it.Metadata, it.Provenance, jsonObject(it.Text)} {
		for _, k := range repositoryKeys {
			if s, ok := c[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// ValidateCases applies the V2 benchmark protocol rules.
func ValidateCases(cases []Case, research bool) error {
	for n, c := range cases {
		i := n + 1
		fail := func(format string, a ...any) error {
			return fmt.Errorf("benchmark case %d "+format, append([]any{i}, a...)...)
		}
		if c.Str("task") == "" {
			return fail("must define a non-empty task")
		}
		k, err := c.Int("retrieval_k", 5)
		if err != nil {
			return fail("retrieval_k must be an integer")
		}
		if k < 1 {
			return fail("retrieval_k must be >= 1")
		}
		taskType := c.Str("task_type")
		if _, present := c["task_type"]; present && c["task_type"] != nil {
			if !slices.Contains(append(append([]string{}, positiveTaskTypes...), "no_gold"), taskType) {
				return fail("task_type must be one of [code2test comment2context edit2ripple no_gold trace2code]")
			}
		} else if research {
			return fail("must define task_type")
		}
		control := c.Str("control_type")
		if control == "" {
			control = "positive"
		}
		if !slices.Contains(controlTypes, control) {
			return fail("control_type must be one of [natural_no_gold positive wrong_repo]")
		}
		schema, err := c.Int("schema_version", 1)
		if err != nil {
			return fail("schema_version must be an integer")
		}
		if schema != 1 && schema != 2 {
			return fail("schema_version must be 1 or 2")
		}
		gold, ok := c.Strings("gold_files")
		if !ok {
			return fail("gold_files must be a list of paths")
		}
		goldSet := map[string]bool{}
		for _, g := range gold {
			goldSet[NormalizePath(g)] = true
		}
		relevance := c.list("file_relevance")
		if _, present := c["file_relevance"]; present && c["file_relevance"] != nil && relevance == nil {
			return fail("file_relevance must be a list of objects")
		}
		relPaths := map[string]bool{}
		for j, raw := range relevance {
			row, ok := raw.(map[string]any)
			if !ok {
				return fail("file_relevance must be a list of objects")
			}
			p, ok := insideRelative(fmt.Sprint(orEmpty(row["path"])))
			if !ok {
				return fail("file relevance %d path must stay inside its repository", j+1)
			}
			if !slices.Contains(relevanceRoles, strings.TrimSpace(fmt.Sprint(orEmpty(row["role"])))) {
				return fail("file relevance %d role must be one of [edit_target supporting_context]", j+1)
			}
			if relPaths[p] {
				return fail("file_relevance paths must be unique")
			}
			relPaths[p] = true
		}
		if len(relevance) > 0 && !sameSet(relPaths, goldSet) {
			return fail("file_relevance must label every gold_file exactly once")
		}
		distractors, ok := c.Strings("distractor_files")
		if !ok {
			return fail("distractor_files must be a list of paths")
		}
		seenD := map[string]bool{}
		for _, d := range distractors {
			p, ok := insideRelative(d)
			if !ok {
				return fail("distractor file must stay inside its repository")
			}
			if seenD[p] {
				return fail("distractor_files must be unique")
			}
			if goldSet[p] {
				return fail("distractor_files must be disjoint from gold_files")
			}
			seenD[p] = true
		}
		spans := c.list("gold_spans")
		if _, present := c["gold_spans"]; present && c["gold_spans"] != nil && spans == nil {
			return fail("gold_spans must be a list of objects")
		}
		for j, raw := range spans {
			s, ok := raw.(map[string]any)
			if !ok {
				return fail("gold_spans must be a list of objects")
			}
			sc := Case(s)
			if NormalizePath(sc.Str("path")) == "" {
				return fail("gold span %d must define path", j+1)
			}
			if _, ok := insideRelative(sc.Str("path")); !ok {
				return fail("gold span %d path must stay inside its repository", j+1)
			}
			start, e1 := sc.Int("start_line", 0)
			end, e2 := sc.Int("end_line", 0)
			if e1 != nil || e2 != nil {
				return fail("gold span %d lines must be integers", j+1)
			}
			if start < 1 || end < start {
				return fail("gold span %d must satisfy 1 <= start_line <= end_line", j+1)
			}
			if d := sc.Str("content_sha256"); s["content_sha256"] != nil && !sha256RE.MatchString(d) {
				return fail("gold span %d content_sha256 must be 64 hex digits", j+1)
			}
		}
		positive := slices.Contains(positiveTaskTypes, taskType)
		if positive && len(gold) == 0 {
			return fail("task_type=%s requires gold_files", taskType)
		}
		if schema == 2 && research && positive && len(spans) == 0 {
			return fail("schema v2 positive cases require gold_spans")
		}
		if control != "positive" && (len(gold) > 0 || len(spans) > 0 || len(relevance) > 0 || len(distractors) > 0) {
			return fail("selective controls must not define gold files, spans, relevance roles, or distractors")
		}
		if schema == 2 {
			for _, f := range []string{"case_id", "repository_id", "language", "label_source"} {
				if c.Str(f) == "" {
					return fail("schema v2 requires %s", f)
				}
			}
			lc, err := c.Int("labeler_count", 0)
			if err != nil {
				return fail("labeler_count must be an integer")
			}
			if lc < 1 {
				return fail("schema v2 requires labeler_count >= 1")
			}
			if control != "positive" && c.Str("control_source") == "" {
				return fail("schema v2 selective controls require control_source provenance")
			}
			for _, f := range []string{"budget_tokens", "budget_lines"} {
				if c[f] == nil {
					continue
				}
				v, err := c.Int(f, 0)
				if err != nil {
					return fail("%s must be an integer", f)
				}
				if v < 1 {
					return fail("%s must be >= 1", f)
				}
			}
		}
		if c["base_commit"] != nil && !gitSHA.MatchString(c.Str("base_commit")) {
			return fail("base_commit must be a full 40-64 digit Git object ID")
		}
		if research && c.Str("base_commit") == "" {
			return fail("must define base_commit")
		}
		if c["content_manifest_sha256"] != nil && !sha256RE.MatchString(c.Str("content_manifest_sha256")) {
			return fail("content_manifest_sha256 must be 64 hex digits")
		}
		if schema == 2 && research && c.Str("content_manifest_sha256") == "" {
			return fail("schema v2 requires content_manifest_sha256")
		}
		rp := c.Str("repository_path")
		if rp == "" {
			rp = "."
		}
		if filepath.IsAbs(rp) || strings.HasPrefix(rp, "/") || slices.Contains(strings.Split(filepath.ToSlash(rp), "/"), "..") {
			return fail("repository_path must stay inside the benchmark root")
		}
		forbidden, ok := c.Strings("forbidden_repositories")
		if !ok {
			return fail("forbidden_repositories must be a list of identities")
		}
		if control == "wrong_repo" && research && len(forbidden) == 0 {
			return fail("wrong_repo control requires forbidden_repositories")
		}
	}
	return nil
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// RepositoryPath returns the case repository path ("." by default).
func (c Case) RepositoryPath() string {
	if rp := c.Str("repository_path"); rp != "" {
		return rp
	}
	return "."
}

// ResolveCaseRoot confines the case repository to the benchmark root.
func ResolveCaseRoot(root string, c Case) (string, error) {
	rp := c.RepositoryPath()
	if rp == "." {
		return root, nil
	}
	rel, abs, err := withinRoot(root, rp)
	if err != nil || rel == "" {
		return "", errors.New("benchmark repository_path must stay inside the benchmark root")
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return "", fmt.Errorf("benchmark repository_path does not exist: %s", rp)
	}
	return abs, nil
}

func withinRoot(root, rel string) (string, string, error) {
	r, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	abs := filepath.Join(r, filepath.FromSlash(rel))
	if ev, err := filepath.EvalSymlinks(abs); err == nil {
		abs = ev
	}
	if ev, err := filepath.EvalSymlinks(r); err == nil {
		r = ev
	}
	out, err := filepath.Rel(r, abs)
	if err != nil || out == ".." || strings.HasPrefix(out, ".."+string(filepath.Separator)) {
		return "", "", errors.New("outside root")
	}
	return filepath.ToSlash(out), abs, nil
}

func git(dir string, args ...string) ([]byte, bool) {
	out, err := procx.Output(context.Background(), procx.Cmd{Argv: append([]string{"git"}, args...), Dir: dir, Env: procx.InheritEnv(),
		Timeout: 5 * time.Second, MaxStdout: 256 << 20})
	return out, err == nil
}

// CaptureSnapshot records HEAD, worktree cleanliness and the tracked-tree digest.
func CaptureSnapshot(repo string) map[string]any {
	head, ok1 := git(repo, "rev-parse", "HEAD")
	status, ok2 := git(repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	tree, ok3 := git(repo, "ls-tree", "-r", "-z", "HEAD")
	if !ok1 || !ok2 || !ok3 {
		return map[string]any{"status": "unavailable", "actual_head": nil, "worktree_clean": nil, "content_manifest_sha256": nil}
	}
	sum := sha256.Sum256(tree)
	return map[string]any{"status": "captured", "actual_head": strings.TrimSpace(string(head)), "worktree_clean": len(status) == 0,
		"content_manifest_sha256": hex.EncodeToString(sum[:])}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SnapshotStatus compares a case's frozen identity with the checkout.
func SnapshotStatus(root string, c Case) map[string]any {
	expected := c.Str("base_commit")
	manifest := strings.ToLower(c.Str("content_manifest_sha256"))
	rp := c.RepositoryPath()
	base := map[string]any{"repository_path": rp, "expected_head": nilIfEmpty(expected), "actual_head": nil, "worktree_clean": nil,
		"expected_content_manifest_sha256": nilIfEmpty(manifest), "content_manifest_sha256": nil}
	if expected == "" {
		base["status"], base["content_manifest_match"], base["match"] = "not_declared", nil, nil
		return base
	}
	repo, err := ResolveCaseRoot(root, c)
	if err != nil {
		base["status"], base["content_manifest_match"], base["match"] = "invalid_path", false, false
		return base
	}
	snap := CaptureSnapshot(repo)
	if snap["status"] != "captured" {
		base["status"], base["content_manifest_match"], base["match"] = "unavailable", false, false
		return base
	}
	actual, clean, got := snap["actual_head"].(string), snap["worktree_clean"].(bool), snap["content_manifest_sha256"].(string)
	headMatch := actual == expected
	manifestMatch := manifest == "" || manifest == got
	status := "match"
	switch {
	case !headMatch:
		status = "head_mismatch"
	case !clean:
		status = "dirty_worktree"
	case !manifestMatch:
		status = "content_mismatch"
	}
	base["status"], base["actual_head"], base["head_match"], base["worktree_clean"] = status, actual, headMatch, clean
	base["content_manifest_sha256"], base["content_manifest_match"], base["match"] = got, manifestMatch, headMatch && clean && manifestMatch
	return base
}
