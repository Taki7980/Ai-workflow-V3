// Package telemetry writes privacy-preserving local retrieval traces and
// summarizes them (V2 telemetry.py parity). Task text is never stored locally.
package telemetry

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/storage"
)

const (
	TracesRelative       = "ai-workspace/generated/traces"
	HMACKeyEnv           = "AI_WORKFLOW_TELEMETRY_HMAC_KEY"
	OTLPEndpointEnv      = "AI_WORKFLOW_OTLP_ENDPOINT"
	OTLPAllowedHostsEnv  = "AI_WORKFLOW_OTLP_ALLOWED_HOSTS"
	OTLPHeadersJSONEnv   = "AI_WORKFLOW_OTLP_HEADERS_JSON"
	OTLPTimeoutEnv       = "AI_WORKFLOW_OTLP_TIMEOUT_SECONDS"
	OTLPIncludeTaskEnv   = "AI_WORKFLOW_OTLP_INCLUDE_TASK_TEXT"
	defaultRetentionDays = 30
	defaultMaxTraceFiles = 200
	maxTraceReadBytes    = 1 << 20
)

// Trace is one retrieval run (V2 RetrievalTrace). Task is never persisted.
type Trace struct {
	Task                 string             `json:"-"`
	Lane                 model.Lane         `json:"lane"`
	Risk                 model.Risk         `json:"risk"`
	Intent               string             `json:"intent"`
	RunID                string             `json:"run_id"`
	PolicyIdentity       map[string]any     `json:"policy_identity"`
	WorkspaceFingerprint string             `json:"workspace_fingerprint"`
	GraphFingerprint     string             `json:"graph_fingerprint"`
	StartedAt            string             `json:"started_at"`
	ProvidersAttempted   []string           `json:"providers_attempted"`
	ProvidersSkipped     map[string]string  `json:"providers_skipped"`
	StageLatencyMS       map[string]float64 `json:"stage_latency_ms"`
	Candidates           map[string]int     `json:"candidates"`
	Selected             map[string]int     `json:"selected"`
	Sufficiency          map[string]any     `json:"sufficiency"`
	Fallbacks            []string           `json:"fallbacks"`
	StaleRejections      int                `json:"stale_rejections"`
	BudgetChars          int                `json:"budget_chars"`
	UsedChars            int                `json:"used_chars"`
}

// Enabled reports whether a run in lane should be traced (V2 trace_enabled).
func Enabled(cfg config.Config, lane model.Lane, explicit bool) bool {
	if explicit {
		return true
	}
	switch strings.ToLower(cfg.Context.Telemetry.Mode) {
	case "off":
		return false
	case "all":
		return true
	}
	return lane != model.LaneAnswer
}

// payload converts t into the redacted map that is persisted or exported.
func payload(t Trace, cfg config.Config, includeTask bool) map[string]any {
	b, _ := json.Marshal(t)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if key := os.Getenv(HMACKeyEnv); key != "" {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(t.Task))
		m["task_fingerprint"] = hex.EncodeToString(mac.Sum(nil))
	}
	var patterns []*regexp.Regexp
	for _, p := range cfg.Context.Telemetry.RedactPatterns {
		if re, err := regexp.Compile(p); err == nil {
			patterns = append(patterns, re)
		}
	}
	if includeTask {
		m["task"] = t.Task
	}
	return redact(m, patterns).(map[string]any)
}

func redact(v any, patterns []*regexp.Regexp) any {
	switch x := v.(type) {
	case string:
		for _, re := range patterns {
			x = re.ReplaceAllString(x, "[REDACTED]")
		}
		return x
	case []any:
		for i := range x {
			x[i] = redact(x[i], patterns)
		}
	case map[string]any:
		for k := range x {
			x[k] = redact(x[k], patterns)
		}
	}
	return v
}

// Write persists t under ai-workspace/generated/traces, prunes old traces,
// best-effort exports to a trusted OTLP endpoint, and returns the relative path.
func Write(ctx context.Context, root string, cfg config.Config, t Trace) (string, error) {
	if t.StartedAt == "" {
		t.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	dir := filepath.Join(root, filepath.FromSlash(TracesRelative))
	name := fmt.Sprintf("%s-%d.json", time.Now().UTC().Format("20060102T150405.000000000Z"), os.Getpid())
	if err := storage.WriteJSON(filepath.Join(dir, name), payload(t, cfg, false)); err != nil {
		return "", err
	}
	prune(dir, cfg)
	if s, ok := trustedOTLP(); ok {
		_ = exportOTLP(ctx, s, payload(t, cfg, s.includeTask)) // export is best-effort
	}
	return TracesRelative + "/" + name, nil
}

func prune(dir string, cfg config.Config) {
	retention := defaultRetentionDays
	if cfg.Context.Telemetry.RetentionDays != nil {
		retention = max(0, *cfg.Context.Telemetry.RetentionDays)
	}
	maxFiles := defaultMaxTraceFiles
	if cfg.Context.Telemetry.MaxTraceFiles > 0 {
		maxFiles = cfg.Context.Telemetry.MaxTraceFiles
	}
	entries, err := traceFiles(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Duration(retention) * 24 * time.Hour)
	kept := 0
	for _, e := range entries { // newest first
		if (retention > 0 && e.mod.Before(cutoff)) || kept >= maxFiles {
			_ = os.Remove(e.path)
			continue
		}
		kept++
	}
}

type traceFile struct {
	path string
	mod  time.Time
}

func traceFiles(dir string) ([]traceFile, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []traceFile{}
	for _, d := range des {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") || strings.HasPrefix(d.Name(), ".") {
			continue
		}
		if info, err := d.Info(); err == nil {
			out = append(out, traceFile{filepath.Join(dir, d.Name()), info.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].mod.Equal(out[j].mod) {
			return out[i].mod.After(out[j].mod)
		}
		return out[i].path > out[j].path
	})
	return out, nil
}

// readTraces loads up to limit newest traces, skipping unreadable or oversized files.
func readTraces(root string, limit int) []map[string]any {
	files, err := traceFiles(filepath.Join(root, filepath.FromSlash(TracesRelative)))
	if err != nil {
		return nil
	}
	out := []map[string]any{}
	for _, f := range files {
		if len(out) >= max(1, limit) {
			break
		}
		if info, err := os.Stat(f.path); err != nil || info.Size() > maxTraceReadBytes {
			continue
		}
		b, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		var m map[string]any
		if json.Unmarshal(b, &m) == nil && m != nil {
			out = append(out, m)
		}
	}
	return out
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 1 {
		return round(sorted[0], 2)
	}
	pos := float64(len(sorted)-1) * q
	lo := int(pos)
	hi := min(lo+1, len(sorted)-1)
	frac := pos - float64(lo)
	return round(sorted[lo]*(1-frac)+sorted[hi]*frac, 2)
}

// Summarize implements `stats` (V2 summarize_traces).
func Summarize(root string, limit int) map[string]any {
	records := readTraces(root, limit)
	byIntent := map[string]int{}
	stages := map[string][]float64{}
	var utilSum float64
	utilN, fallbacks := 0, 0
	for _, r := range records {
		intent, _ := r["intent"].(string)
		if intent == "" {
			intent = "unknown"
		}
		byIntent[intent]++
		if budget := num(r["budget_chars"]); budget > 0 {
			utilSum += num(r["used_chars"]) / budget
			utilN++
		}
		if fb, _ := r["fallbacks"].([]any); len(fb) > 0 {
			fallbacks++
		}
		if lat, ok := r["stage_latency_ms"].(map[string]any); ok {
			for k, v := range lat {
				stages[k] = append(stages[k], num(v))
			}
		}
	}
	latency := map[string]any{}
	for name, vals := range stages {
		sort.Float64s(vals)
		sum := 0.0
		for _, v := range vals {
			sum += v
		}
		latency[name] = map[string]float64{
			"p50": percentile(vals, .5), "p95": percentile(vals, .95), "p99": percentile(vals, .99),
			"mean": round(sum/float64(len(vals)), 2),
		}
	}
	util, rate := 0.0, 0.0
	if utilN > 0 {
		util = round(utilSum/float64(utilN), 4)
	}
	if len(records) > 0 {
		rate = round(float64(fallbacks)/float64(len(records)), 4)
	}
	return map[string]any{"runs": len(records), "by_intent": byIntent, "mean_budget_utilization": util, "fallback_rate": rate, "latency_ms": latency}
}

// Recommend implements `stats --recommend` (V2 policy_recommendations).
// Output is advisory only and never changes configuration.
func Recommend(root string, limit, minimumRuns int) map[string]any {
	records := readTraces(root, limit)
	recs := []map[string]any{}
	if len(records) < minimumRuns {
		return map[string]any{"runs": len(records), "minimum_runs": minimumRuns, "recommendations": recs, "note": "insufficient observations; no policy recommendation produced"}
	}
	byIntent := map[string][]map[string]any{}
	for _, r := range records {
		intent, _ := r["intent"].(string)
		if intent == "" {
			intent = "unknown"
		}
		byIntent[intent] = append(byIntent[intent], r)
	}
	intents := make([]string, 0, len(byIntent))
	for k := range byIntent {
		intents = append(intents, k)
	}
	sort.Strings(intents)
	for _, intent := range intents {
		rows := byIntent[intent]
		if len(rows) < max(5, minimumRuns/5) {
			continue
		}
		fb, suff := 0.0, 0.0
		for _, r := range rows {
			if f, _ := r["fallbacks"].([]any); len(f) > 0 {
				fb++
			}
			if s, ok := r["sufficiency"].(map[string]any); ok {
				suff += num(s["score"])
			}
		}
		fb /= float64(len(rows))
		suff /= float64(len(rows))
		if fb >= .5 {
			recs = append(recs, map[string]any{"intent": intent, "signal": "high_fallback_rate", "observed": round(fb, 4), "action": "review provider availability, query-intent rules, or retrieval threshold before changing policy"})
		}
		if suff >= .9 {
			recs = append(recs, map[string]any{"intent": intent, "signal": "consistently_high_sufficiency", "observed": round(suff, 4), "action": "benchmark a smaller soft context fraction; keep the hard lane ceiling unchanged"})
		}
	}
	return map[string]any{"runs": len(records), "minimum_runs": minimumRuns, "recommendations": recs, "note": "recommendations are advisory only and never change deterministic safety routing"}
}

type otlpSettings struct {
	endpoint    string
	timeout     time.Duration
	headers     map[string]string
	includeTask bool
}

// trustedOTLP reads export settings from the environment only (never repo
// config): HTTPS, host on the explicit allowlist, no credentials in the URL,
// and no loopback/link-local/multicast/unspecified literal destinations.
func trustedOTLP() (otlpSettings, bool) {
	raw := strings.TrimSpace(os.Getenv(OTLPEndpointEnv))
	if raw == "" {
		return otlpSettings{}, false
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil {
		return otlpSettings{}, false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" || unsafeHost(host) {
		return otlpSettings{}, false
	}
	allowed := false
	for _, h := range strings.Split(os.Getenv(OTLPAllowedHostsEnv), ",") {
		if strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), ".")) == host {
			allowed = true
		}
	}
	if !allowed {
		return otlpSettings{}, false
	}
	secs, err := strconv.ParseFloat(os.Getenv(OTLPTimeoutEnv), 64)
	if err != nil {
		secs = 2
	}
	secs = math.Min(10, math.Max(.05, secs))
	headers := map[string]string{}
	var parsed map[string]any
	if json.Unmarshal([]byte(os.Getenv(OTLPHeadersJSONEnv)), &parsed) == nil {
		for k, v := range parsed {
			if strings.TrimSpace(k) != "" {
				headers[k] = fmt.Sprint(v)
			}
		}
	}
	inc := strings.ToLower(strings.TrimSpace(os.Getenv(OTLPIncludeTaskEnv)))
	return otlpSettings{raw, time.Duration(secs * float64(time.Second)), headers, inc == "1" || inc == "true" || inc == "yes" || inc == "on"}, true
}

func unsafeHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified())
}

func exportOTLP(ctx context.Context, s otlpSettings, p map[string]any) error {
	body, err := json.Marshal(map[string]any{"resourceLogs": []any{map[string]any{"scopeLogs": []any{map[string]any{"logRecords": []any{map[string]any{"body": p}}}}}}})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects are not followed") }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
