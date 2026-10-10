package bench

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Corpus is a corpus-v2 document.
type Corpus map[string]any

// LoadJSON decodes a JSON file preserving numbers.
func LoadJSON(p string) (any, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return v, nil
}

func (c Corpus) cases() []Case {
	out := []Case{}
	l, _ := c["cases"].([]any)
	for _, raw := range l {
		m, _ := raw.(map[string]any)
		out = append(out, Case(m))
	}
	return out
}

// ValidateCorpus enforces corpus-v2 metadata plus research-protocol cases.
func ValidateCorpus(c Corpus) error {
	if n, _ := Case(c).Int("schema_version", 0); n != 2 {
		return errors.New("benchmark corpus schema_version must be 2")
	}
	if Case(c).Str("corpus_id") == "" {
		return errors.New("benchmark corpus must define corpus_id")
	}
	src, ok := c["source"].(map[string]any)
	if !ok {
		return errors.New("benchmark corpus must define source metadata")
	}
	for _, f := range []string{"name", "url", "license"} {
		if Case(src).Str(f) == "" {
			return fmt.Errorf("benchmark corpus source must define %s", f)
		}
	}
	if Case(c).Str("split") == "" {
		return errors.New("benchmark corpus must define split")
	}
	l, ok := c["cases"].([]any)
	if !ok {
		return errors.New("benchmark corpus cases must be a list")
	}
	for i, raw := range l {
		if _, ok := raw.(map[string]any); !ok {
			return fmt.Errorf("benchmark case %d must be an object", i+1)
		}
	}
	return ValidateCases(c.cases(), true)
}

// CorpusSummary reports counts and publication readiness.
func CorpusSummary(c Corpus) (map[string]any, error) {
	if err := ValidateCorpus(c); err != nil {
		return nil, err
	}
	taskTypes, controls, languages := map[string]int{}, map[string]int{}, map[string]int{}
	repos := map[string]bool{}
	spanCases := 0
	for _, cs := range c.cases() {
		taskTypes[orDefault(cs.Str("task_type"), "unknown")]++
		controls[orDefault(cs.Str("control_type"), "positive")]++
		languages[orDefault(cs.Str("language"), "unknown")]++
		if len(cs.list("gold_spans")) > 0 {
			spanCases++
		}
		if r := cs.Str("repository_id"); r != "" {
			repos[r] = true
		}
	}
	s := map[string]any{"schema_version": 2, "corpus_id": c["corpus_id"], "split": c["split"], "source": c["source"],
		"cases": len(c.cases()), "span_labeled_cases": spanCases, "task_types": taskTypes, "control_types": controls,
		"languages": languages, "repositories": len(repos)}
	blockers := []string{}
	if len(c.cases()) < 427 {
		blockers = append(blockers, "insufficient_cases")
	}
	if len(repos) < 25 {
		blockers = append(blockers, "insufficient_repositories")
	}
	if len(languages) < 4 {
		blockers = append(blockers, "insufficient_language_diversity")
	}
	if controls["natural_no_gold"] < 1 {
		blockers = append(blockers, "missing_natural_no_gold_controls")
	}
	if controls["wrong_repo"] < 1 {
		blockers = append(blockers, "missing_wrong_repo_controls")
	}
	if spanCases < 1 {
		blockers = append(blockers, "missing_span_labels")
	}
	s["publication_readiness"] = map[string]any{"ready": len(blockers) == 0, "reference_floor": "Agent Retrieval Bench scale floor",
		"minimum_cases": 427, "minimum_repositories": 25, "minimum_languages": 4, "blockers": blockers}
	return s, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func canonicalSHA(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

var splitAliases = map[string]string{"development": "development", "dev": "development", "train": "development",
	"calibration": "calibration", "calibrate": "calibration", "validation": "calibration", "val": "calibration",
	"holdout": "holdout", "test": "holdout", "evaluation": "holdout", "eval": "holdout", "ci": "ci", "smoke": "ci"}
var protectedSplits = []string{"development", "calibration", "holdout"}
var historyIsolation = map[string]bool{"git_metadata_removed": true, "exported_tree": true, "sandboxed_no_history": true}
var taskTokenRE = regexp.MustCompile(`[A-Za-z0-9_./:-]+`)

func isProtected(s string) bool {
	for _, p := range protectedSplits {
		if s == p {
			return true
		}
	}
	return false
}

func splitRole(c Corpus) (string, error) {
	if e := strings.ToLower(Case(c).Str("split_role")); e != "" {
		if !isProtected(e) && e != "ci" && e != "other" {
			return "", errors.New("benchmark corpus split_role must be development, calibration, holdout, ci, or other")
		}
		return e, nil
	}
	return splitAliases[strings.ToLower(Case(c).Str("split"))], nil
}

func normalizedTask(s string) string {
	return strings.Join(taskTokenRE.FindAllString(strings.ToLower(s), -1), " ")
}

func taskFingerprint(c Case) string {
	return canonicalSHA(map[string]any{"task": normalizedTask(c.Str("task")), "task_type": strings.ToLower(c.Str("task_type")),
		"control_type": strings.ToLower(orDefault(c.Str("control_type"), "positive"))})
}

func sourceFingerprint(c Case) string {
	spans := []map[string]any{}
	for _, raw := range c.list("gold_spans") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		s, e := Case(m).Int("start_line", 0)
		en, _ := Case(m).Int("end_line", 0)
		_ = e
		spans = append(spans, map[string]any{"path": NormalizePath(Case(m).Str("path")), "start_line": s, "end_line": en})
	}
	sort.Slice(spans, func(i, j int) bool {
		a, b := spans[i], spans[j]
		if a["path"] != b["path"] {
			return a["path"].(string) < b["path"].(string)
		}
		if a["start_line"] != b["start_line"] {
			return a["start_line"].(int) < b["start_line"].(int)
		}
		return a["end_line"].(int) < b["end_line"].(int)
	})
	gold, _ := c.Strings("gold_files")
	normalized := []string{}
	for _, g := range gold {
		normalized = append(normalized, NormalizePath(g))
	}
	sort.Strings(normalized)
	return canonicalSHA(map[string]any{"repository_id": c.Str("repository_id"), "base_commit": strings.ToLower(c.Str("base_commit")),
		"gold_files": normalized, "gold_spans": spans})
}

func temporal(c Case) (string, bool) {
	for _, f := range []string{"source_event_time", "issue_created_at", "created_at", "observed_at"} {
		v := c.Str(f)
		if v == "" {
			continue
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04:05.999999", "2006-01-02"} {
			if _, err := time.Parse(layout, v); err == nil {
				return f, true
			}
		}
		return f, false
	}
	return "", false
}

func hints(c Case) ([]string, []string) {
	task := strings.ToLower(c.Str("task"))
	gold, _ := c.Strings("gold_files")
	exposed := map[string]bool{}
	for _, g := range gold {
		p := NormalizePath(g)
		if p == "" {
			continue
		}
		if base := path.Base(p); strings.Contains(task, strings.ToLower(p)) {
			exposed[p] = true
		} else if len(base) >= 6 && strings.Contains(task, strings.ToLower(base)) {
			exposed[base] = true
		}
	}
	repo := c.Str("repository_id")
	repoHints := map[string]bool{}
	if repo != "" {
		cands := []string{repo}
		if i := strings.LastIndex(repo, "/"); i >= 0 {
			cands = append(cands, repo[i+1:])
		}
		for _, h := range cands {
			if len(h) >= 5 && strings.Contains(task, strings.ToLower(h)) {
				repoHints[h] = true
			}
		}
	}
	return sortedSet(exposed), sortedSet(repoHints)
}

func issue(code, msg string, corpora, splits, cases, repos []string) map[string]any {
	norm := func(xs []string) []string {
		m := map[string]bool{}
		for _, x := range xs {
			m[x] = true
		}
		return sortedSet(m)
	}
	return map[string]any{"code": code, "message": msg, "corpora": norm(corpora), "splits": norm(splits), "case_ids": norm(cases), "repositories": norm(repos)}
}

type caseRecord struct{ corpus, split, caseID, repo, task, fingerprint string }

// Integrity runs the cross-split leakage gate (V2 analyze_partition_integrity).
func Integrity(docs []Corpus) (map[string]any, error) {
	if len(docs) == 0 {
		return nil, errors.New("benchmark integrity requires at least one corpus")
	}
	blockers, signals, rows := []map[string]any{}, []map[string]any{}, []map[string]any{}
	records := []caseRecord{}
	byID, byTask, bySource := map[string][]caseRecord{}, map[string][]caseRecord{}, map[string][]caseRecord{}
	reposBySplit := map[string]map[string]bool{}
	for _, d := range docs {
		if err := ValidateCorpus(d); err != nil {
			return nil, err
		}
		id := Case(d).Str("corpus_id")
		role, err := splitRole(d)
		if err != nil {
			return nil, err
		}
		if role == "" {
			signals = append(signals, issue("unclassified_split", "Corpus split is not mapped to a protected split role.", []string{id}, []string{Case(d).Str("split")}, nil, nil))
		}
		hi := Case(d).Str("history_isolation")
		rows = append(rows, map[string]any{"corpus_id": id, "split": d["split"], "split_role": orDefault(role, "unclassified"),
			"document_sha256": canonicalSHA(d), "cases": len(d.cases()), "history_isolation": nilIfEmpty(hi)})
		if role == "holdout" && !historyIsolation[hi] {
			signals = append(signals, issue("holdout_history_isolation_unverified", "Holdout corpus does not attest that future Git history is unavailable to evaluated agents.", []string{id}, []string{role}, nil, nil))
		}
		split := orDefault(role, "unclassified")
		for _, c := range d.cases() {
			cid, repo := c.Str("case_id"), c.Str("repository_id")
			rec := caseRecord{id, split, cid, repo, c.Str("task"), taskFingerprint(c)}
			records = append(records, rec)
			one := []string{cid}
			if isProtected(role) && cid == "" {
				blockers = append(blockers, issue("missing_stable_case_id", "Protected benchmark case requires a stable case_id.", []string{id}, []string{role}, nil, []string{repo}))
			}
			byID[cid] = append(byID[cid], rec)
			byTask[rec.fingerprint] = append(byTask[rec.fingerprint], rec)
			src := sourceFingerprint(c)
			bySource[src] = append(bySource[src], rec)
			if isProtected(role) && repo != "" {
				if reposBySplit[role] == nil {
					reposBySplit[role] = map[string]bool{}
				}
				reposBySplit[role][repo] = true
			}
			if isProtected(role) {
				if field, valid := temporal(c); field == "" {
					signals = append(signals, issue("missing_temporal_provenance", "Protected benchmark case lacks source-event time metadata.", []string{id}, []string{role}, one, []string{repo}))
				} else if !valid {
					blockers = append(blockers, issue("invalid_temporal_provenance", "Benchmark case has malformed source-event time metadata.", []string{id}, []string{role}, one, []string{repo}))
				}
			}
			gold, repoHints := hints(c)
			if len(gold) > 0 {
				s := issue("gold_path_exposed_in_task", "Task text exposes one or more gold file hints.", []string{id}, []string{split}, one, []string{repo})
				s["hints"] = gold
				signals = append(signals, s)
			}
			if len(repoHints) > 0 {
				s := issue("repository_identity_exposed_in_task", "Task text exposes repository identity hints.", []string{id}, []string{split}, one, []string{repo})
				s["hints"] = repoHints
				signals = append(signals, s)
			}
			if role == "holdout" {
				if c.Str("base_commit") == "" {
					blockers = append(blockers, issue("holdout_missing_base_commit", "Holdout case must pin a full base commit.", []string{id}, []string{role}, one, []string{repo}))
				}
				if c.Str("content_manifest_sha256") == "" {
					blockers = append(blockers, issue("holdout_missing_content_manifest", "Holdout case must pin tracked-content identity.", []string{id}, []string{role}, one, []string{repo}))
				}
			}
		}
	}
	cols := func(rs []caseRecord) (corp, split, ids, repos []string) {
		for _, r := range rs {
			corp, split, ids, repos = append(corp, r.corpus), append(split, r.split), append(ids, r.caseID), append(repos, r.repo)
		}
		return
	}
	protectedOf := func(rs []caseRecord) []string {
		m := map[string]bool{}
		for _, r := range rs {
			if isProtected(r.split) {
				m[r.split] = true
			}
		}
		return sortedSet(m)
	}
	for _, id := range sortedKeysOf(byID) {
		if rs := byID[id]; id != "" && len(rs) > 1 {
			c, s, _, r := cols(rs)
			blockers = append(blockers, issue("duplicate_case_id", fmt.Sprintf("case_id appears in %d corpus entries.", len(rs)), c, s, []string{id}, r))
		}
	}
	for _, k := range sortedKeysOf(byTask) {
		if rs := byTask[k]; len(rs) > 1 && len(protectedOf(rs)) > 1 {
			c, _, ids, r := cols(rs)
			blockers = append(blockers, issue("cross_split_task_duplicate", "Normalized task fingerprint appears across protected splits.", c, protectedOf(rs), ids, r))
		}
	}
	for i, l := range records {
		if !isProtected(l.split) {
			continue
		}
		lt := taskTokens(l.task)
		for _, r := range records[i+1:] {
			if !isProtected(r.split) || l.split == r.split || l.fingerprint == r.fingerprint {
				continue
			}
			if sim := jaccardStrings(lt, taskTokens(r.task)); sim >= .85 {
				s := issue("cross_split_near_duplicate_task", "Task wording is highly similar across protected splits.",
					[]string{l.corpus, r.corpus}, []string{l.split, r.split}, []string{l.caseID, r.caseID}, []string{l.repo, r.repo})
				s["similarity"] = float64(int(sim*1e4+.5)) / 1e4
				signals = append(signals, s)
			}
		}
	}
	for _, k := range sortedKeysOf(bySource) {
		if rs := bySource[k]; len(rs) > 1 && len(protectedOf(rs)) > 1 {
			c, _, ids, r := cols(rs)
			blockers = append(blockers, issue("cross_split_source_instance_duplicate", "Same repository/base/gold source instance appears across protected splits.", c, protectedOf(rs), ids, r))
		}
	}
	for i, a := range protectedSplits {
		for _, b := range protectedSplits[i+1:] {
			overlap := []string{}
			for r := range reposBySplit[a] {
				if reposBySplit[b][r] {
					overlap = append(overlap, r)
				}
			}
			if len(overlap) > 0 {
				blockers = append(blockers, issue("cross_split_repository_overlap", "Protected splits must be repository-disjoint.", nil, []string{a, b}, nil, overlap))
			}
		}
	}
	present := map[string]bool{}
	for _, r := range rows {
		if s, _ := r["split_role"].(string); isProtected(s) {
			present[s] = true
		}
	}
	missing := []string{}
	for _, s := range protectedSplits {
		if !present[s] {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		signals = append(signals, issue("incomplete_partition_set", "Integrity report does not include every protected split role.", nil, missing, nil, nil))
	}
	sort.Slice(rows, func(i, j int) bool { return fmt.Sprint(rows[i]["corpus_id"]) < fmt.Sprint(rows[j]["corpus_id"]) })
	bySplit := map[string][]string{}
	for _, s := range protectedSplits {
		bySplit[s] = sortedSet(reposBySplit[s])
	}
	return map[string]any{"schema_version": 1, "ready": len(blockers) == 0, "corpora": rows, "protected_splits": protectedSplits,
		"cases": len(records), "repositories_by_split": bySplit, "blockers": blockers, "leakage_signals": signals,
		"limitations": []string{
			"This gate detects repository/corpus metadata leakage; it cannot prove whether a model saw benchmark data during pretraining.",
			"Gold/repository hints are reported as signals because real issue descriptions may legitimately mention files or repository names.",
			"Temporal provenance coverage supports contamination analysis but does not establish a proprietary model's training cutoff.",
			"History-isolation is an attestation in corpus metadata; execution environments must still enforce that Git history is inaccessible.",
		}}, nil
}

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func taskTokens(s string) map[string]bool {
	m := map[string]bool{}
	for _, t := range strings.Fields(normalizedTask(s)) {
		m[t] = true
	}
	if len(m) < 6 {
		return map[string]bool{}
	}
	return m
}

func jaccardStrings(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}
