package handoff

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
)

func write(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rendered() string {
	d := model.RouteDecision{Lane: model.LaneFull, Risk: model.RiskHigh}
	return Render(d, "native", []string{"a", "a", "b"}, "  goal ")
}

func TestValidateMissing(t *testing.T) {
	got := Validate(t.TempDir(), 30)
	if !slices.Equal(got, []string{"missing ai-workspace/handoff/HANDOFF.md"}) {
		t.Fatalf("got %q", got)
	}
}

func TestValidateRenderedIsValid(t *testing.T) {
	root := t.TempDir()
	text := rendered()
	if !strings.Contains(text, "- **Context sources**: a, b\n") || !strings.Contains(text, "- **Goal / state**: goal / routed\n") {
		t.Fatalf("unexpected render:\n%s", text)
	}
	write(t, root, RelativePath, text)
	if got := Validate(root, 30); len(got) != 0 {
		t.Fatalf("rendered handoff invalid: %q", got)
	}
}

func TestRenderNoSources(t *testing.T) {
	if !strings.Contains(Render(model.RouteDecision{Lane: model.LaneSmall, Risk: model.RiskLow}, "native", nil, "g"), "- **Context sources**: none\n") {
		t.Fatal("empty sources must render none")
	}
}

func TestValidateErrors(t *testing.T) {
	root := t.TempDir()
	write(t, root, RelativePath, strings.Repeat("x\n", 31))
	got := Validate(root, 30)
	if !slices.Contains(got, "handoff has 31 lines; cap is 30") || !slices.Contains(got, "missing field: Lane / risk") {
		t.Fatalf("got %q", got)
	}
	write(t, root, RelativePath, rendered()+"[TODO later]\n")
	if got := Validate(root, 30); !slices.Contains(got, "handoff still contains template placeholders") {
		t.Fatalf("got %q", got)
	}
}

func TestValidateLegacyFallback(t *testing.T) {
	root := t.TempDir()
	write(t, root, LegacyRelativePath, rendered())
	if got := Validate(root, 30); len(got) != 0 {
		t.Fatalf("legacy handoff not validated: %q", got)
	}
}

func TestValidateCRLF(t *testing.T) {
	root := t.TempDir()
	text := rendered()
	write(t, root, RelativePath, strings.ReplaceAll(text, "\n", "\r\n"))
	lines := strings.Count(text, "\n")
	if got := Validate(root, lines); len(got) != 0 {
		t.Fatalf("CRLF handoff at exact cap invalid: %q", got)
	}
	if got := Validate(root, lines-1); len(got) != 1 {
		t.Fatalf("CRLF line count wrong: %q", got)
	}
}
