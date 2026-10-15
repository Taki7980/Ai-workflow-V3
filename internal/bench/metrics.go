package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/stats"
)

func estimateTokens(s string) int { return (len(s) + 3) / 4 }

func dcgTerm(rank int) float64 { return 1 / math.Log2(float64(rank)+1) }

// PatternMetrics is V2 retrieval_metrics over case-folded gold patterns.
func PatternMetrics(items []model.ContextItem, patterns []string, k int) map[string]any {
	ps := []string{}
	for _, p := range patterns {
		if strings.TrimSpace(p) != "" {
			ps = append(ps, strings.ToLower(p))
		}
	}
	if len(ps) == 0 {
		return nil
	}
	cutoff := max(1, k)
	ranked := items[:min(len(items), cutoff)]
	relevant, first := 0, 0
	unmatched := map[int]bool{}
	for i := range ps {
		unmatched[i] = true
	}
	next, dcg := 1, 0.0
	for rank, it := range ranked {
		text := strings.ToLower(it.Text)
		hit := false
		for _, p := range ps {
			hit = hit || strings.Contains(text, p)
		}
		if hit {
			relevant++
			if first == 0 {
				first = rank + 1
			}
		}
		for i := range ps {
			if unmatched[i] && strings.Contains(text, ps[i]) {
				pos := max(rank+1, next)
				dcg += dcgTerm(pos)
				next = pos + 1
				delete(unmatched, i)
			}
		}
	}
	idealTerms := []float64{}
	for r := 1; r <= len(ps); r++ {
		idealTerms = append(idealTerms, dcgTerm(r))
	}
	ideal := stats.PySum(idealTerms)
	mrr := 0.0
	if first > 0 {
		mrr = 1 / float64(first)
	}
	covered := len(ps) - len(unmatched)
	return map[string]any{"k": cutoff, "relevant_items": relevant, "matched_patterns": covered,
		"relevant_item_density": float64(relevant) / float64(max(1, len(ranked))), "precision_at_k": float64(relevant) / float64(cutoff),
		"recall_at_k": float64(covered) / float64(len(ps)), "mrr": mrr, "ndcg_at_k": dcg / ideal}
}

func rankedFiles(items []model.ContextItem, cutoff int) []string {
	out, seen := []string{}, map[string]bool{}
	for _, it := range items {
		p := ItemFile(it)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) >= cutoff {
			break
		}
	}
	return out
}

func uniqueNormalized(xs []string) []string {
	out, seen := []string{}, map[string]bool{}
	for _, x := range xs {
		if n := NormalizePath(x); strings.TrimSpace(x) != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// FileMetrics is V2 file_retrieval_metrics.
func FileMetrics(items []model.ContextItem, goldFiles []string, k int) map[string]any {
	gold := uniqueNormalized(goldFiles)
	if len(gold) == 0 {
		return nil
	}
	cutoff := max(1, k)
	files := rankedFiles(items, cutoff)
	matched, first, dcgTerms := []string{}, 0, []float64{}
	for rank, f := range files {
		if slices.Contains(gold, f) {
			matched = append(matched, f)
			dcgTerms = append(dcgTerms, dcgTerm(rank+1))
			if first == 0 {
				first = rank + 1
			}
		}
	}
	idealTerms := []float64{}
	for r := 1; r <= min(len(gold), cutoff); r++ {
		idealTerms = append(idealTerms, dcgTerm(r))
	}
	dcg, ideal := stats.PySum(dcgTerms), stats.PySum(idealTerms)
	precision := float64(len(matched)) / float64(cutoff)
	recall := float64(len(matched)) / float64(len(gold))
	f1 := 0.0
	if precision+recall > 0 {
		f1 = 2 * precision * recall / (precision + recall)
	}
	mrr := 0.0
	if first > 0 {
		mrr = 1 / float64(first)
	}
	return map[string]any{"k": cutoff, "gold_files": gold, "retrieved_files": files, "matched_gold_files": matched,
		"precision_at_k": precision, "recall_at_k": recall, "mrr": mrr, "ndcg_at_k": dcg / ideal, "file_f1": f1}
}

// RepositoryControlMetrics measures wrong-repository contamination.
func RepositoryControlMetrics(items []model.ContextItem, forbidden []string, k int) map[string]any {
	set := map[string]bool{}
	for _, f := range forbidden {
		if f = strings.TrimSpace(f); f != "" {
			set[f] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	cutoff := max(1, k)
	observed, contaminated, inspected := []string{}, 0, 0
	for _, it := range items[:min(len(items), cutoff)] {
		inspected++
		id := ItemRepository(it)
		if id == "" {
			continue
		}
		if !slices.Contains(observed, id) {
			observed = append(observed, id)
		}
		if set[id] {
			contaminated++
		}
	}
	names := make([]string, 0, len(set))
	for f := range set {
		names = append(names, f)
	}
	sort.Strings(names)
	return map[string]any{"k": cutoff, "identity_coverage": float64(len(observedAll(items, cutoff))) / float64(max(1, inspected)),
		"observed_repositories": observed, "forbidden_repositories": names, "contamination_count": contaminated,
		"contamination_rate": float64(contaminated) / float64(cutoff)}
}

// observedAll counts items (not unique repositories) that carry an identity.
func observedAll(items []model.ContextItem, cutoff int) []string {
	out := []string{}
	for _, it := range items[:min(len(items), cutoff)] {
		if id := ItemRepository(it); id != "" {
			out = append(out, id)
		}
	}
	return out
}

var roleGains = map[string]int{"edit_target": 2, "supporting_context": 1}

func ptr(f float64) *float64 { return &f }

// RoleMetrics is V2 role_aware_file_metrics.
func RoleMetrics(items []model.ContextItem, relevance []any, distractorFiles []string, k int) map[string]any {
	if len(relevance) == 0 && len(distractorFiles) == 0 {
		return nil
	}
	roles := map[string]string{}
	for _, raw := range relevance {
		row, _ := raw.(map[string]any)
		p := NormalizePath(fmt.Sprint(orEmpty(row["path"])))
		r := strings.TrimSpace(fmt.Sprint(orEmpty(row["role"])))
		if p != "" && roleGains[r] > 0 {
			roles[p] = r
		}
	}
	distractors := map[string]bool{}
	for _, d := range distractorFiles {
		if strings.TrimSpace(d) != "" {
			distractors[NormalizePath(d)] = true
		}
	}
	cutoff := max(1, k)
	files := rankedFiles(items, cutoff)
	edit, support := sortedKeys(roles, "edit_target"), sortedKeys(roles, "supporting_context")
	var mEdit, mSupport, mDistract []string
	firstEdit, dcgTerms, weighted := 0, []float64{}, 0
	for rank, f := range files {
		switch roles[f] {
		case "edit_target":
			mEdit = append(mEdit, f)
			if firstEdit == 0 {
				firstEdit = rank + 1
			}
		case "supporting_context":
			mSupport = append(mSupport, f)
		}
		if g := roleGains[roles[f]]; g > 0 {
			dcgTerms = append(dcgTerms, (math.Pow(2, float64(g))-1)/math.Log2(float64(rank+1)+1))
			weighted += g
		}
		if distractors[f] {
			mDistract = append(mDistract, f)
		}
	}
	gains := []int{}
	total := 0
	for _, r := range roles {
		gains = append(gains, roleGains[r])
		total += roleGains[r]
	}
	sort.Sort(sort.Reverse(sort.IntSlice(gains)))
	idealTerms := []float64{}
	for i, g := range gains[:min(len(gains), cutoff)] {
		idealTerms = append(idealTerms, (math.Pow(2, float64(g))-1)/math.Log2(float64(i+1)+1))
	}
	dcg, ideal := stats.PySum(dcgTerms), stats.PySum(idealTerms)
	out := map[string]any{"k": cutoff, "edit_target_files": edit, "supporting_context_files": support,
		"known_distractor_files": sortedSet(distractors), "retrieved_files": files, "matched_edit_targets": orEmptyList(mEdit),
		"matched_supporting_context": orEmptyList(mSupport), "matched_known_distractors": orEmptyList(mDistract)}
	var editRecall, supportRecall *float64
	if len(edit) > 0 {
		editRecall = ptr(float64(len(mEdit)) / float64(len(edit)))
	}
	if len(support) > 0 {
		supportRecall = ptr(float64(len(mSupport)) / float64(len(support)))
	}
	out["edit_target_recall_at_k"], out["supporting_context_recall_at_k"] = editRecall, supportRecall
	out["edit_target_mrr"], out["weighted_recall_at_k"], out["graded_ndcg_at_k"] = (*float64)(nil), (*float64)(nil), (*float64)(nil)
	if firstEdit > 0 {
		out["edit_target_mrr"] = ptr(1 / float64(firstEdit))
	}
	if total > 0 {
		out["weighted_recall_at_k"] = ptr(float64(weighted) / float64(total))
	}
	if ideal > 0 {
		out["graded_ndcg_at_k"] = ptr(dcg / ideal)
	}
	out["useful_precision_at_k"], out["known_distractor_rate_at_k"], out["coverage_balance"] = (*float64)(nil), (*float64)(nil), (*float64)(nil)
	if len(roles) > 0 {
		out["useful_precision_at_k"] = ptr(float64(len(mEdit)+len(mSupport)) / float64(cutoff))
	}
	if len(distractors) > 0 {
		out["known_distractor_rate_at_k"] = ptr(float64(len(mDistract)) / float64(cutoff))
	}
	if editRecall != nil && supportRecall != nil {
		out["coverage_balance"] = ptr(min(*editRecall, *supportRecall))
	}
	return out
}

func orEmptyList(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func sortedKeys(m map[string]string, want string) []string {
	out := []string{}
	for k, v := range m {
		if v == want {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sortedSet(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type span struct {
	Path       string  `json:"path"`
	Start      int     `json:"start_line"`
	End        int     `json:"end_line"`
	Repository *string `json:"repository_id"`
	Digest     *string `json:"content_sha256,omitempty"`
}

var spanTextRE = regexp.MustCompile(`^(.+?):(\d+)(?::(\d+))?:\s`)

func lineValue(c map[string]any, keys ...string) int {
	for _, k := range keys {
		switch v := c[k].(type) {
		case float64:
			if v >= 1 {
				return int(v)
			}
		case int:
			if v >= 1 {
				return v
			}
		case int64:
			if v >= 1 {
				return int(v)
			}
		case json.Number:
			if n, err := strconv.Atoi(v.String()); err == nil && n >= 1 {
				return n
			}
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 1 {
				return n
			}
		}
	}
	return 0
}

func itemSpan(it model.ContextItem) (span, bool) {
	p := ItemFile(it)
	if p == "" {
		return span{}, false
	}
	start, end := 0, 0
	for _, c := range []map[string]any{it.Metadata, it.Provenance, jsonObject(it.Text)} {
		if start == 0 {
			start = lineValue(c, "line", "line_number", "start_line", "line_start")
		}
		if end == 0 {
			end = lineValue(c, "end_line", "line_end")
		}
	}
	if m := spanTextRE.FindStringSubmatch(it.Text); m != nil {
		if start == 0 {
			start, _ = strconv.Atoi(m[2])
		}
		if end == 0 && m[3] != "" {
			end, _ = strconv.Atoi(m[3])
		}
	}
	if start == 0 {
		return span{}, false
	}
	if end < start {
		end = start
	}
	var repo *string
	if r := ItemRepository(it); r != "" {
		repo = &r
	}
	return span{Path: p, Start: start, End: end, Repository: repo}, true
}

func overlap(r, g span) (int, int, bool) {
	if r.Path != g.Path || (g.Repository != nil && (r.Repository == nil || *r.Repository != *g.Repository)) {
		return 0, 0, false
	}
	s, e := max(r.Start, g.Start), min(r.End, g.End)
	return s, e, s <= e
}

// SpanMetrics is V2 span_retrieval_metrics with an optional line budget.
func SpanMetrics(items []model.ContextItem, goldRaw []any, k int, lineBudget int) (map[string]any, error) {
	if len(goldRaw) == 0 {
		return nil, nil
	}
	gold, seen := []span{}, map[string]bool{}
	for _, raw := range goldRaw {
		m, _ := raw.(map[string]any)
		c := Case(m)
		p := NormalizePath(c.Str("path"))
		start, _ := c.Int("start_line", 0)
		end, _ := c.Int("end_line", 0)
		if p == "" || start < 1 || end < start {
			return nil, errors.New("gold span lines must satisfy 1 <= start <= end")
		}
		s := span{Path: p, Start: start, End: end}
		if r := c.Str("repository_id"); r != "" {
			s.Repository = &r
		}
		if d := strings.ToLower(c.Str("content_sha256")); d != "" {
			s.Digest = &d
		}
		key := fmt.Sprint(s.Repository != nil, deref(s.Repository), s.Path, s.Start, s.End)
		if !seen[key] {
			seen[key] = true
			gold = append(gold, s)
		}
	}
	cutoff := max(1, k)
	remaining := -1
	if lineBudget > 0 {
		remaining = lineBudget
	}
	ranked := []span{}
	for _, it := range items {
		s, ok := itemSpan(it)
		if !ok {
			continue
		}
		if remaining >= 0 {
			if remaining == 0 {
				break
			}
			if n := s.End - s.Start + 1; n > remaining {
				s.End = s.Start + remaining - 1
				remaining = 0
			} else {
				remaining -= n
			}
		}
		ranked = append(ranked, s)
		if len(ranked) >= cutoff || remaining == 0 {
			break
		}
	}
	hits, first := 0, 0
	for i, r := range ranked {
		for _, g := range gold {
			if _, _, ok := overlap(r, g); ok {
				hits++
				if first == 0 {
					first = i + 1
				}
				break
			}
		}
	}
	matched := []span{}
	totalGold, covered := 0, 0
	for _, g := range gold {
		totalGold += g.End - g.Start + 1
		intervals := [][2]int{}
		for _, r := range ranked {
			if s, e, ok := overlap(r, g); ok {
				intervals = append(intervals, [2]int{s, e})
			}
		}
		if len(intervals) > 0 {
			matched = append(matched, g)
			sort.Slice(intervals, func(i, j int) bool {
				return intervals[i][0] < intervals[j][0] || intervals[i][0] == intervals[j][0] && intervals[i][1] < intervals[j][1]
			})
			cs, ce := intervals[0][0], intervals[0][1]
			for _, iv := range intervals[1:] {
				if iv[0] <= ce+1 {
					ce = max(ce, iv[1])
					continue
				}
				covered += ce - cs + 1
				cs, ce = iv[0], iv[1]
			}
			covered += ce - cs + 1
		}
	}
	totalRetrieved, relevantRetrieved := 0, 0
	for _, r := range ranked {
		totalRetrieved += r.End - r.Start + 1
		lines := map[int]bool{}
		for _, g := range gold {
			if s, e, ok := overlap(r, g); ok {
				for l := s; l <= e; l++ {
					lines[l] = true
				}
			}
		}
		relevantRetrieved += len(lines)
	}
	precision := float64(hits) / float64(cutoff)
	recall := float64(len(matched)) / float64(len(gold))
	f1 := 0.0
	if precision+recall > 0 {
		f1 = 2 * precision * recall / (precision + recall)
	}
	linePrecision, lineRecall := 0.0, 0.0
	if totalRetrieved > 0 {
		linePrecision = float64(relevantRetrieved) / float64(totalRetrieved)
	}
	if totalGold > 0 {
		lineRecall = float64(covered) / float64(totalGold)
	}
	var firstRank, budget any
	if first > 0 {
		firstRank = first
	}
	if lineBudget > 0 {
		budget = lineBudget
	}
	return map[string]any{"k": cutoff, "gold_spans": gold, "retrieved_spans": ranked, "matched_gold_spans": matched,
		"span_precision_at_k": precision, "span_recall_at_k": recall, "span_f1": f1, "line_precision": linePrecision,
		"line_recall": lineRecall, "covered_gold_lines": covered, "total_gold_lines": totalGold, "first_gold_rank": firstRank,
		"line_budget": budget, "retrieved_lines": totalRetrieved}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type event struct {
	Kind string
	File string
	Step int
}

// LoadTrajectory reads inline trajectory_events or a confined trajectory_file.
func LoadTrajectory(root string, c Case) ([]event, error) {
	inline, hasInline := c["trajectory_events"]
	file := c.Str("trajectory_file")
	if hasInline && inline != nil && file != "" {
		return nil, errors.New("benchmark case must define either trajectory_events or trajectory_file, not both")
	}
	var rows []any
	switch {
	case hasInline && inline != nil:
		l, ok := inline.([]any)
		if !ok {
			return nil, errors.New("trajectory_events must be an array")
		}
		rows = l
	case file != "":
		rel, ok := insideRelative(file)
		if !ok {
			return nil, errors.New("trajectory path must stay inside the benchmark root")
		}
		_, abs, err := withinRoot(root, rel)
		if err != nil {
			return nil, errors.New("trajectory_file must stay inside the benchmark root")
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("unable to read trajectory_file: %s", rel)
		}
		if strings.EqualFold(path.Ext(rel), ".jsonl") {
			for n, line := range strings.Split(string(b), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				var v any
				if json.Unmarshal([]byte(line), &v) != nil {
					return nil, fmt.Errorf("invalid JSONL trajectory at line %d: %s", n+1, rel)
				}
				rows = append(rows, v)
			}
		} else if err := json.Unmarshal(b, &rows); err != nil {
			return nil, fmt.Errorf("invalid JSON trajectory: %s", rel)
		}
	default:
		return nil, nil
	}
	out := []event{}
	for i, raw := range rows {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("trajectory event %d must be an object", i+1)
		}
		ec := Case(m)
		kind := strings.ToLower(ec.Str("kind"))
		if kind != "seed" && kind != "explored" && kind != "utilized" {
			return nil, fmt.Errorf("trajectory event %d kind must be one of [explored seed utilized]", i+1)
		}
		f, ok := m["file"].(string)
		if !ok || strings.TrimSpace(f) == "" {
			return nil, fmt.Errorf("trajectory event %d must define file", i+1)
		}
		if _, isBool := m["step"].(bool); isBool {
			return nil, fmt.Errorf("trajectory event %d step must be an integer", i+1)
		}
		step, err := ec.Int("step", i+1)
		if err != nil {
			return nil, fmt.Errorf("trajectory event %d step must be an integer", i+1)
		}
		if step < 0 {
			return nil, fmt.Errorf("trajectory event %d step must be non-negative", i+1)
		}
		out = append(out, event{kind, NormalizePath(f), step})
	}
	return out, nil
}

func uniq(xs []string) []string {
	out, seen := []string{}, map[string]bool{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func frac(n, d int) any {
	if d == 0 {
		return nil
	}
	return float64(n) / float64(d)
}

// TrajectoryMetrics separates exploration from utilization (V2 parity).
func TrajectoryMetrics(events []event, goldFiles []string) map[string]any {
	if len(events) == 0 {
		return nil
	}
	gold := map[string]bool{}
	for _, g := range goldFiles {
		if strings.TrimSpace(g) != "" {
			gold[NormalizePath(g)] = true
		}
	}
	ordered := append([]event{}, events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Step != ordered[j].Step {
			return ordered[i].Step < ordered[j].Step
		}
		return ordered[i].Kind < ordered[j].Kind
	})
	files := map[string][]string{}
	lastSeed := -1
	for _, e := range ordered {
		files[e.Kind] = append(files[e.Kind], e.File)
		if e.Kind == "seed" {
			lastSeed = max(lastSeed, e.Step)
		}
	}
	seed, explored, utilized := uniq(files["seed"]), uniq(files["explored"]), uniq(files["utilized"])
	count := func(xs []string, in map[string]bool) int {
		n := 0
		for _, x := range xs {
			if in[x] {
				n++
			}
		}
		return n
	}
	setOf := func(xs []string) map[string]bool {
		m := map[string]bool{}
		for _, x := range xs {
			m[x] = true
		}
		return m
	}
	exploredGold, utilizedGold, seedGold := count(explored, gold), count(utilized, gold), count(seed, gold)
	var firstExplore, firstUtil any
	postSeed := map[string]bool{}
	for _, e := range ordered {
		if e.Kind == "explored" && gold[e.File] && firstExplore == nil {
			firstExplore = e.Step
		}
		if e.Kind == "utilized" && gold[e.File] && firstUtil == nil {
			firstUtil = e.Step
		}
		if e.Kind == "explored" && lastSeed >= 0 && e.Step > lastSeed {
			postSeed[e.File] = true
		}
	}
	dup := 0.0
	if len(files["explored"]) > 0 {
		dup = 1 - float64(len(explored))/float64(len(files["explored"]))
	}
	return map[string]any{"events": len(ordered), "seed_unique_files": seed, "explored_unique_files": explored,
		"utilized_unique_files": utilized, "seed_gold_recall": frac(seedGold, len(gold)),
		"exploration_precision": frac(exploredGold, len(explored)), "exploration_recall": frac(exploredGold, len(gold)),
		"utilization_precision": frac(utilizedGold, len(utilized)), "utilization_recall": frac(utilizedGold, len(gold)),
		"context_utilization_rate": frac(count(utilized, setOf(explored)), len(explored)),
		"gold_utilization_rate":    frac(utilizedGold, exploredGold), "duplicate_exploration_rate": dup,
		"first_gold_exploration_step": firstExplore, "first_gold_utilization_step": firstUtil,
		"post_seed_exploration_unique_files": len(postSeed)}
}

func ratio(n, d int) any {
	if d <= 0 {
		return nil
	}
	return math.Round(float64(n)/float64(d)*1e4) / 1e4
}

func tokensOf(items []model.ContextItem) int {
	n := 0
	for _, it := range items {
		n += estimateTokens(it.Text)
	}
	return n
}

// TokenEconomics is V2 retrieval_token_funnel + benchmark_token_economics.
func TokenEconomics(retrieved, selected []model.ContextItem, c Case, hardBudget, adaptiveBudget, calls int, latencyMS float64) map[string]any {
	unique, seen := []model.ContextItem{}, map[[32]byte]bool{}
	for _, it := range retrieved {
		if k := it.DedupeKey(); !seen[k] {
			seen[k] = true
			unique = append(unique, it)
		}
	}
	ranked := unique
	rt, ut, kt, st := tokensOf(retrieved), tokensOf(unique), tokensOf(ranked), tokensOf(selected)
	reduction := any(nil)
	if rt > 0 {
		reduction = math.Round((1-float64(st)/float64(rt))*1e4) / 1e4
	}
	out := map[string]any{"estimator": "provider_neutral_estimate", "retrieved_item_count": len(retrieved), "retrieved_estimated_tokens": rt,
		"unique_retrieved_item_count": len(unique), "unique_retrieved_estimated_tokens": ut, "duplicate_item_count": len(retrieved) - len(unique),
		"duplicate_estimated_tokens": rt - ut, "duplicate_token_fraction": ratio(rt-ut, rt), "ranked_candidate_item_count": len(ranked),
		"ranked_candidate_estimated_tokens": kt, "selected_item_count": len(selected), "selected_estimated_tokens": st,
		"selected_vs_retrieved_token_ratio": ratio(st, rt), "selected_vs_ranked_token_ratio": ratio(st, kt),
		"estimated_token_reduction_vs_retrieved": reduction, "hard_budget_tokens": max(0, hardBudget), "adaptive_budget_tokens": max(0, adaptiveBudget),
		"selected_hard_budget_utilization": ratio(st, hardBudget), "selected_adaptive_budget_utilization": ratio(st, adaptiveBudget),
		"adaptive_budget_fraction": ratio(adaptiveBudget, hardBudget), "retrieval_call_count": max(0, calls), "latency_ms": latencyMS}
	gold, distract, roles := map[string]bool{}, map[string]bool{}, map[string]string{}
	if g, ok := c.Strings("gold_files"); ok {
		for _, x := range g {
			gold[NormalizePath(x)] = true
		}
	}
	if d, ok := c.Strings("distractor_files"); ok {
		for _, x := range d {
			distract[NormalizePath(x)] = true
		}
	}
	for _, raw := range c.list("file_relevance") {
		if row, ok := raw.(map[string]any); ok {
			roles[NormalizePath(fmt.Sprint(orEmpty(row["path"])))] = strings.TrimSpace(fmt.Sprint(orEmpty(row["role"])))
		}
	}
	var gt, et, sp, dt int
	for _, it := range selected {
		p := ItemFile(it)
		if p == "" {
			continue
		}
		n := estimateTokens(it.Text)
		if gold[p] {
			gt += n
		}
		switch roles[p] {
		case "edit_target":
			et += n
		case "supporting_context":
			sp += n
		}
		if distract[p] {
			dt += n
		}
	}
	un := max(0, st-min(st, gt+dt))
	out["selected_gold_file_estimated_tokens"], out["selected_edit_target_estimated_tokens"] = gt, et
	out["selected_supporting_context_estimated_tokens"], out["selected_known_distractor_estimated_tokens"] = sp, dt
	out["selected_unattributed_estimated_tokens"] = un
	out["gold_file_token_share"], out["edit_target_token_share"] = ratio(gt, st), ratio(et, st)
	out["supporting_context_token_share"], out["known_distractor_token_share"] = ratio(sp, st), ratio(dt, st)
	out["unattributed_token_share"] = ratio(un, st)
	out["gold_file_tokens_per_1k_retrieved"] = nil
	if rt > 0 {
		out["gold_file_tokens_per_1k_retrieved"] = math.Round(float64(gt)*1000/float64(rt)*1e4) / 1e4
	}
	return out
}
