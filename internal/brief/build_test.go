package brief

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/handoff"
	"github.com/Taki7980/ai-workflow-v3/internal/indexer"
	"github.com/Taki7980/ai-workflow-v3/internal/storage"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

// newWorkspace creates a git-backed control root with files, a config,
// registry and index, mirroring `ai-workflow setup`.
func newWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	for rel, text := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	reindex(t, root)
	return root
}

func reindex(t *testing.T, root string) {
	t.Helper()
	ctx := context.Background()
	repos, err := workspace.Discover(ctx, root, 8, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Save(root, repos); err != nil {
		t.Fatal(err)
	}
	reg := workspace.Registry{Version: workspace.RegistryVersion, ReviewRequired: true, Repositories: repos}
	if _, err := indexer.BuildWorkspaceWithMode(ctx, root, reg, indexer.BuildFull); err != nil {
		t.Fatal(err)
	}
}

const payGo = "package pay\n\n// RetryPayment retries with backoff.\nfunc RetryPayment() error {\n\treturn nil\n}\n"

func firstSource(p Packet, source string) (string, bool) {
	for _, it := range p.Context {
		if it.Source == source {
			return it.Text, true
		}
	}
	return "", false
}

func TestBriefPacketShape(t *testing.T) {
	root := newWorkspace(t, map[string]string{"pay.go": payGo})
	p, err := Build(context.Background(), root, "fix RetryPayment backoff", Options{})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := firstSource(p, "lightweight_index")
	if !ok || !strings.HasPrefix(text, `{"symbol":"RetryPayment"`) || !strings.Contains(text, "func RetryPayment() error {") {
		t.Fatalf("index item = %q (context %+v)", text, p.Context)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(p.Retrieval.WorkspaceState.Fingerprint) {
		t.Fatalf("fingerprint %q", p.Retrieval.WorkspaceState.Fingerprint)
	}
	if p.Lane == "answer" {
		t.Fatalf("mutation routed to answer: %+v", p.RouteDecision)
	}
	if _, err := os.Stat(filepath.Join(root, "ai-workspace", "generated", "last-brief.json")); err != nil {
		t.Fatal("last-brief.json not written")
	}
	if p.EstimatedContextTokensUsed <= 0 || p.EstimatedContextTokensUsed > p.Retrieval.HardContextTokens {
		t.Fatalf("token accounting %d / %d", p.EstimatedContextTokensUsed, p.Retrieval.HardContextTokens)
	}
}

func TestBriefFingerprintChangesOnEdit(t *testing.T) {
	root := newWorkspace(t, map[string]string{"pay.go": payGo})
	opt := Options{ChangedFiles: []string{"pay.go"}}
	a, _ := Build(context.Background(), root, "fix RetryPayment", opt)
	b, _ := Build(context.Background(), root, "fix RetryPayment", opt)
	if a.Retrieval.WorkspaceState.Fingerprint != b.Retrieval.WorkspaceState.Fingerprint {
		t.Fatal("fingerprint unstable")
	}
	if err := os.WriteFile(filepath.Join(root, "pay.go"), []byte(payGo+"// edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ := Build(context.Background(), root, "fix RetryPayment", opt)
	if c.Retrieval.WorkspaceState.Fingerprint == a.Retrieval.WorkspaceState.Fingerprint {
		t.Fatal("fingerprint ignored edit")
	}
}

func TestBriefStaleSnippetDropped(t *testing.T) {
	root := newWorkspace(t, map[string]string{"pay.go": payGo})
	if err := os.WriteFile(filepath.Join(root, "pay.go"), []byte(payGo+"// edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Build(context.Background(), root, "fix RetryPayment", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := firstSource(p, "lightweight_index"); ok {
		t.Fatalf("stale index item served: %q", text)
	}
}

func TestBriefMissingIndexDegrades(t *testing.T) {
	root := newWorkspace(t, map[string]string{"pay.go": payGo})
	reg, _ := workspace.Load(root)
	if err := os.Remove(indexer.Path(root, reg.Repositories[0])); err != nil {
		t.Fatal(err)
	}
	p, err := Build(context.Background(), root, "fix RetryPayment", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.Retrieval.Fallbacks, "index unavailable: .") {
		t.Fatalf("fallbacks %q", p.Retrieval.Fallbacks)
	}
}

func TestBriefTargetedSourceFallback(t *testing.T) {
	root := newWorkspace(t, map[string]string{"pay.go": payGo})
	old := rgPath
	rgPath = func() (string, error) { return "", errors.New("missing") }
	t.Cleanup(func() { rgPath = old })
	p, err := Build(context.Background(), root, "fix RetryPayment", Options{})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := firstSource(p, "targeted_source")
	if !ok || !strings.HasPrefix(text, "pay.go:") {
		t.Fatalf("targeted = %q context %+v", text, p.Context)
	}
	if !slices.Contains(p.Retrieval.Fallbacks, "ripgrep unavailable; file scan used") {
		t.Fatalf("fallbacks %q", p.Retrieval.Fallbacks)
	}
}

func TestBriefAnswerAbstains(t *testing.T) {
	root := newWorkspace(t, map[string]string{"main.go": "package main\n"})
	p, err := Build(context.Background(), root, "explain how quantum flux works", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Lane != "answer" || p.Retrieval.EvidenceState != "abstain" {
		t.Fatalf("lane %s evidence %s", p.Lane, p.Retrieval.EvidenceState)
	}
	if _, err := os.Stat(filepath.Join(root, "ai-workspace", "generated", "last-brief.json")); err == nil {
		t.Fatal("answer lane must not write last-brief.json")
	}
}

func TestBriefWriteHandoff(t *testing.T) {
	root := newWorkspace(t, map[string]string{"pay.go": payGo})
	p, err := Build(context.Background(), root, "refactor payment retry architecture", Options{WriteHandoff: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.HandoffWritten != handoff.RelativePath {
		t.Fatalf("handoff_written %q", p.HandoffWritten)
	}
	if errs := handoff.Validate(root, 30); len(errs) != 0 {
		t.Fatalf("written handoff invalid: %q", errs)
	}
}

func TestBriefCRLFSnippet(t *testing.T) {
	root := newWorkspace(t, map[string]string{"pay.go": strings.ReplaceAll(payGo, "\n", "\r\n")})
	p, err := Build(context.Background(), root, "fix RetryPayment", Options{})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := firstSource(p, "lightweight_index")
	if !ok || strings.Contains(text, "\r") {
		t.Fatalf("CRLF snippet %q", text)
	}
}

func TestBriefIncludesFreshMemory(t *testing.T) {
	root := newWorkspace(t, map[string]string{"pay.go": payGo})
	rec := `{"id":"mem-aaaaaaaaaaaa","type":"decision","created_at":"x","verified_at":"x","keywords":["retrypayment","retry","payment"],"summary":"RetryPayment uses jittered backoff","evidence":"","files":[],"source_hashes":{},"confidence":0.9}` + "\n"
	if err := storage.WriteFileAtomic(filepath.Join(root, "ai-workspace", "memory", "memory.jsonl"), []byte(rec)); err != nil {
		t.Fatal(err)
	}
	p, err := Build(context.Background(), root, "fix RetryPayment backoff", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := firstSource(p, "durable_memory"); !ok || !strings.Contains(text, "jittered backoff") {
		t.Fatalf("memory item missing: %+v", p.Context)
	}
}
