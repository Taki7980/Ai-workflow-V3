// Package provider runs external retrieval providers over the V2 JSON stdin /
// stdout protocol with launch trust, explicit environment profiles, optional
// OS sandboxing, bounded output, strict parsing and path confinement.
package provider

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/procx"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

const (
	DefaultMaxOutput = 8 << 20
	DefaultMaxStderr = 64 << 10
	ProtocolVersion  = 1

	TrustConfigured = "configured_local_executable"
	TrustRegistry   = "trusted_registry_digest"
)

// restrictedReserved are runtime identity keys a restricted provider may not import.
var restrictedReserved = map[string]bool{"PATH": true, "HOME": true, "USERPROFILE": true, "TMP": true, "TEMP": true, "TMPDIR": true,
	"LANG": true, "LC_ALL": true, "LC_CTYPE": true, "PYTHONUTF8": true, "PYTHONIOENCODING": true}

// Spec is one provider's launch policy.
type Spec struct {
	Name             string        `json:"name"`
	Command          []string      `json:"command"`
	Timeout          time.Duration `json:"-"`
	MaxOutputBytes   int64         `json:"max_output_bytes"`
	MaxStderrBytes   int64         `json:"max_stderr_bytes"`
	Intents          []string      `json:"intents"`
	EnvAllowlist     []string      `json:"env_allowlist"`
	ExecutableTrust  string        `json:"executable_trust"`
	ExecutableSHA256 string        `json:"executable_sha256,omitempty"`
	NeutralCWD       bool          `json:"neutral_cwd"`
	Version          string        `json:"version"`
	RuntimeProfile   string        `json:"runtime_profile"` // compatibility | restricted
	Sandbox          SandboxPolicy `json:"sandbox"`
}

// Validate normalizes defaults and rejects unsafe or malformed specs.
func (s *Spec) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("provider name must not be blank")
	}
	if len(s.Command) == 0 || slices.Contains(s.Command, "") {
		return fmt.Errorf("provider %q command must not be blank", s.Name)
	}
	if s.Timeout <= 0 {
		s.Timeout = 8 * time.Second
	}
	if s.MaxOutputBytes <= 0 {
		s.MaxOutputBytes = DefaultMaxOutput
	}
	if s.MaxStderrBytes <= 0 {
		s.MaxStderrBytes = DefaultMaxStderr
	}
	if len(s.Intents) == 0 {
		s.Intents = []string{"all"}
	}
	if s.ExecutableTrust == "" {
		s.ExecutableTrust = TrustConfigured
	}
	if s.Version == "" {
		s.Version = "unknown"
	}
	s.RuntimeProfile = strings.ToLower(strings.TrimSpace(s.RuntimeProfile))
	switch s.RuntimeProfile {
	case "":
		s.RuntimeProfile = "compatibility"
	case "compatibility":
	case "restricted":
		for _, k := range s.EnvAllowlist {
			if restrictedReserved[strings.ToUpper(k)] {
				return fmt.Errorf("restricted provider env_allowlist may not override runtime key %s", k)
			}
		}
	default:
		return fmt.Errorf("provider %q runtime_profile must be restricted or compatibility", s.Name)
	}
	if s.ExecutableSHA256 != "" {
		d, err := normalizeDigest(s.ExecutableSHA256)
		if err != nil {
			return fmt.Errorf("provider %q executable_sha256 must be a SHA-256 digest", s.Name)
		}
		s.ExecutableSHA256 = d
	}
	return s.Sandbox.Validate()
}

func normalizeDigest(d string) (string, error) {
	d = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "sha256:")
	if _, err := hex.DecodeString(d); err != nil || len(d) != 64 {
		return "", errors.New("invalid sha256 digest")
	}
	return d, nil
}

// Request is the stdin payload sent to a provider.
type Request struct {
	Query        string         `json:"query"`
	Root         string         `json:"root"`
	Limit        int            `json:"limit"`
	Intent       string         `json:"intent"`
	ChangedFiles []string       `json:"changed_files"`
	Metadata     map[string]any `json:"metadata"`
	Timeout      time.Duration  `json:"-"`
	// Defaults are control-plane metadata merged into every returned item.
	Defaults map[string]any `json:"-"`
}

// Result separates evidence from provider failure.
type Result struct {
	Provider        string              `json:"provider"`
	Items           []model.ContextItem `json:"items"`
	LatencyMS       float64             `json:"latency_ms"`
	Error           string              `json:"error,omitempty"`
	ErrorKind       string              `json:"error_kind,omitempty"`
	TimedOut        bool                `json:"timed_out"`
	OutputLimited   bool                `json:"output_limited"`
	ReturnCode      *int                `json:"returncode"`
	StderrTail      string              `json:"stderr_tail,omitempty"`
	StderrTruncated bool                `json:"stderr_truncated"`
	Sandboxed       bool                `json:"sandboxed"`
	SandboxBackend  string              `json:"sandbox_backend,omitempty"`
	SandboxMode     string              `json:"sandbox_mode"`
	SandboxNetwork  string              `json:"sandbox_network"`
	LimitsEnforced  bool                `json:"resource_limits_enforced"`
	SandboxFallback string              `json:"sandbox_fallback_reason,omitempty"`
}

// OK reports a provider run without error.
func (r Result) OK() bool { return r.Error == "" }

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// within reports whether candidate resolves inside root.
func within(root, candidate string) bool {
	r, err1 := filepath.EvalSymlinks(root)
	c, err2 := filepath.EvalSymlinks(candidate)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(r, c)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// verifyExecutable re-checks a registry-pinned executable at launch: absolute,
// not a symlink, outside the repository, and matching its pinned digest.
func verifyExecutable(s Spec, root string) (string, error) {
	exe := s.Command[0]
	if s.ExecutableTrust != TrustRegistry {
		return exe, nil
	}
	if !filepath.IsAbs(exe) {
		return "", errors.New("trusted provider executable must remain an absolute path")
	}
	if fi, err := os.Lstat(exe); err != nil {
		return "", errors.New("trusted provider executable no longer exists")
	} else if fi.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("trusted provider executable path may not be a symlink")
	} else if !fi.Mode().IsRegular() {
		return "", errors.New("trusted provider executable is no longer a regular file")
	}
	if within(root, exe) {
		return "", errors.New("trusted provider executable may not move inside the repository")
	}
	if s.ExecutableSHA256 == "" {
		return "", errors.New("trusted provider executable is missing its pinned digest")
	}
	got, err := sha256File(exe)
	if err != nil || subtle.ConstantTimeCompare([]byte(got), []byte(s.ExecutableSHA256)) != 1 {
		return "", errors.New("trusted provider executable digest mismatch at launch")
	}
	return exe, nil
}

// runtimeEnv builds the provider environment and working directory. The
// returned cleanup removes any temporary runtime directories.
func runtimeEnv(s Spec, req Request) (cwd string, env, writable []string, cleanup func(), err error) {
	cleanup = func() {}
	legacyCWD := req.Root
	if s.ExecutableTrust == TrustRegistry {
		legacyCWD = filepath.Dir(s.Command[0])
	}
	if s.RuntimeProfile != "restricted" && !s.NeutralCWD {
		return legacyCWD, procx.SafeEnv(s.EnvAllowlist...), nil, cleanup, nil
	}
	base, err := os.MkdirTemp("", "ai-workflow-provider-")
	if err != nil {
		return "", nil, nil, cleanup, err
	}
	cleanup = func() { _ = os.RemoveAll(base) }
	mk := func(name string) (string, error) {
		p := filepath.Join(base, name)
		return p, os.Mkdir(p, 0o700)
	}
	cwd = legacyCWD
	if s.NeutralCWD {
		if cwd, err = mk("cwd"); err != nil {
			return "", nil, nil, cleanup, err
		}
		writable = append(writable, cwd)
	}
	if s.RuntimeProfile != "restricted" {
		return cwd, procx.SafeEnv(s.EnvAllowlist...), writable, cleanup, nil
	}
	home, err := mk("home")
	if err != nil {
		return "", nil, nil, cleanup, err
	}
	tmp, err := mk("tmp")
	if err != nil {
		return "", nil, nil, cleanup, err
	}
	writable = append(writable, home, tmp)
	env = []string{}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "PATHEXT", "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC":
			env = append(env, kv)
		}
	}
	env = append(env, "PATH="+filepath.Dir(s.Command[0]), "HOME="+home, "USERPROFILE="+home,
		"TMPDIR="+tmp, "TEMP="+tmp, "TMP="+tmp, "LANG=C", "LC_ALL=C", "LC_CTYPE=C", "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	for _, k := range s.EnvAllowlist {
		if v, ok := os.LookupEnv(k); ok && !restrictedReserved[strings.ToUpper(k)] {
			env = append(env, k+"="+v)
		}
	}
	return cwd, env, writable, cleanup, nil
}

var (
	bearerRE = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	authRE   = regexp.MustCompile(`(?i)\b(authorization\s*:\s*)(?:bearer\s+)?\S+`)
	secretRE = regexp.MustCompile(`(?i)\b(api[-_]?key|token|password|secret)\b(\s*[:=]\s*)([^\s,;]+)`)
)

// redactStderr removes allowlisted env values and common credential shapes.
func redactStderr(s Spec, raw []byte) string {
	out := strings.ToValidUTF8(string(raw), "�")
	for _, k := range s.EnvAllowlist {
		if v := os.Getenv(k); v != "" {
			out = strings.ReplaceAll(out, v, "[REDACTED]")
		}
	}
	out = bearerRE.ReplaceAllString(out, "Bearer [REDACTED]")
	out = authRE.ReplaceAllString(out, "${1}[REDACTED]")
	return secretRE.ReplaceAllString(out, "${1}${2}[REDACTED]")
}

// Run executes spec for req. Failures are returned inside Result so callers
// can degrade to other providers.
func Run(ctx context.Context, s Spec, req Request, source string) Result {
	start := time.Now()
	res := Result{Provider: s.Name, SandboxMode: s.Sandbox.Mode, SandboxNetwork: s.Sandbox.Network}
	fail := func(kind, msg string) Result {
		res.ErrorKind, res.Error = kind, msg
		res.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
		return res
	}
	if err := s.Validate(); err != nil {
		return fail("invalid_spec", err.Error())
	}
	res.SandboxMode, res.SandboxNetwork = s.Sandbox.Mode, s.Sandbox.Network
	if strings.TrimSpace(req.Query) == "" || req.Limit < 1 {
		return fail("invalid_request", "retrieval query must not be blank and limit must be >= 1")
	}
	exe, err := verifyExecutable(s, req.Root)
	if err != nil {
		return fail("provider_trust", err.Error())
	}
	cwd, env, writable, cleanup, err := runtimeEnv(s, req)
	defer cleanup()
	if err != nil {
		return fail("launch", "provider runtime could not be created")
	}
	argv := append([]string{exe}, s.Command[1:]...)
	plan, err := buildSandboxPlan(s.Sandbox, argv, cwd, writable)
	if err != nil {
		return fail("sandbox_unavailable", err.Error())
	}
	res.Sandboxed, res.SandboxBackend, res.LimitsEnforced, res.SandboxFallback = plan.sandboxed, plan.backend, plan.limitsEnforced, plan.fallbackReason

	root, _ := filepath.Abs(req.Root)
	if req.ChangedFiles == nil {
		req.ChangedFiles = []string{}
	}
	if req.Metadata == nil {
		req.Metadata = map[string]any{}
	}
	if req.Intent == "" {
		req.Intent = "mixed"
	}
	payload, _ := json.Marshal(map[string]any{"query": req.Query, "root": root, "limit": req.Limit, "intent": req.Intent,
		"changed_files": req.ChangedFiles, "metadata": req.Metadata})
	timeout := s.Timeout
	if req.Timeout > 0 && req.Timeout < timeout {
		timeout = req.Timeout
	}
	r, err := procx.Run(ctx, procx.Cmd{Argv: plan.argv, Dir: cwd, Env: env, Stdin: append(payload, '\n'),
		Timeout: timeout, MaxStdout: s.MaxOutputBytes, MaxStderr: s.MaxStderrBytes})
	if err != nil {
		return fail("launch", "provider could not be started")
	}
	code := r.ExitCode
	res.ReturnCode = &code
	res.StderrTail, res.StderrTruncated = redactStderr(s, r.Stderr), r.StderrTruncated
	switch {
	case r.StdoutExceeded:
		res.OutputLimited = true
		return fail("output_limit", fmt.Sprintf("provider output exceeded %d bytes", s.MaxOutputBytes))
	case r.TimedOut:
		res.TimedOut = true
		return fail("timeout", fmt.Sprintf("provider timed out after %s", timeout))
	case r.ExitCode != 0:
		return fail("exit", fmt.Sprintf("provider exited with status %d", r.ExitCode))
	case strings.TrimSpace(string(r.Stdout)) == "":
		return fail("empty_output", "provider returned an empty payload")
	}
	items, err := contextItems(root, r.Stdout, source, req.Limit, s, req.Defaults)
	if err != nil {
		return fail("invalid_payload", err.Error())
	}
	res.Items = items
	res.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
	return res
}

func contextItems(root string, raw []byte, source string, limit int, s Spec, defaults map[string]any) ([]model.ContextItem, error) {
	records, err := parseRecords(raw)
	if err != nil {
		return nil, err
	}
	items := []model.ContextItem{}
	for _, rec := range records {
		if len(rec) > MaxItemFields {
			return nil, errors.New("provider item exceeds maximum field count")
		}
		rawText, ok := rec["text"]
		if !ok {
			rawText = rec["content"]
		}
		if rawText == nil {
			rawText = ""
		}
		text, ok := rawText.(string)
		if !ok {
			return nil, errors.New("provider text/content must be a string")
		}
		if len(text) > MaxTextChars {
			return nil, errors.New("provider text exceeds protocol limit")
		}
		if text = strings.TrimSpace(text); text == "" {
			continue
		}
		meta, ok1 := orEmpty(rec["metadata"]).(map[string]any)
		prov, ok2 := orEmpty(rec["provenance"]).(map[string]any)
		if !ok1 || !ok2 {
			return nil, errors.New("provider metadata and provenance must be objects")
		}
		for _, k := range []string{"path", "file", "symbol", "kind", "language"} {
			if v, ok := rec[k]; ok {
				sv, isStr := v.(string)
				if !isStr || len(sv) > MaxFieldChars {
					return nil, fmt.Errorf("provider %s must be a bounded string", k)
				}
				meta[k] = sv
			}
		}
		for _, k := range []string{"line", "end_line"} {
			if v, ok := rec[k]; ok {
				n, isInt := v.(int64)
				if !isInt || n < 1 || n > MaxLine {
					return nil, fmt.Errorf("provider %s must be an integer between 1 and %d", k, MaxLine)
				}
				meta[k] = n
			}
		}
		stale, ok := orFalse(rec["stale"]).(bool)
		if !ok {
			return nil, errors.New("provider stale must be a boolean")
		}
		score, err := providerScore(rec["score"])
		if err != nil {
			return nil, err
		}
		for k, v := range defaults {
			meta[k] = v
		}
		meta["provider"], meta["provider_trust"], meta["trust"] = s.Name, s.ExecutableTrust, "untrusted_repository_content"
		confinePaths(root, meta)
		prov["provider"], prov["trust"] = s.Name, "untrusted_repository_content"
		items = append(items, model.ContextItem{Source: source, Text: text, Score: score, Stale: stale, Metadata: meta, Provenance: prov})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Score > items[j].Score })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func orEmpty(v any) any {
	if v == nil {
		return map[string]any{}
	}
	return v
}

func orFalse(v any) any {
	if v == nil {
		return false
	}
	return v
}

func providerScore(v any) (float64, error) {
	var f float64
	switch x := v.(type) {
	case nil:
		return 0, nil
	case int64:
		f = float64(x)
	case float64:
		f = x
	default:
		return 0, errors.New("provider score must be a JSON number")
	}
	if math.IsNaN(f) || f < 0 || f > 1 {
		return 0, errors.New("provider score must be between 0 and 1")
	}
	return f, nil
}

// confinePaths drops path/file metadata that is absolute, traverses upward,
// or resolves outside root (V2 confine_metadata_paths).
func confinePaths(root string, meta map[string]any) {
	rejected := []string{}
	for _, k := range []string{"path", "file"} {
		v, ok := meta[k]
		if !ok {
			continue
		}
		if s, isStr := v.(string); !isStr || !safeRelative(root, s) {
			delete(meta, k)
			rejected = append(rejected, k)
		}
	}
	if len(rejected) > 0 {
		meta["path_rejected"] = true
		meta["rejected_path_fields"] = rejected
	}
}

func safeRelative(root, p string) bool {
	// Reject absolute, rooted and drive-qualified forms of every OS, not just the host's.
	drive := len(p) >= 2 && p[1] == ':' && p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z'
	if p == "" || strings.ContainsRune(p, 0) || filepath.IsAbs(p) || drive || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return false
	}
	if slices.Contains(strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }), "..") {
		return false
	}
	_, _, err := workspace.Within(root, p)
	return err == nil
}
