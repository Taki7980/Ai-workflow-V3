package memory

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAddAndSearch(t *testing.T) {
	root := t.TempDir()
	rec, err := Add(root, "decision", "PaymentRetry backoff", " use jittered backoff ", "", nil, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"paymentretry", "payment", "retry", "backoff"} {
		if !slices.Contains(rec.Keywords, k) {
			t.Fatalf("keywords %q missing %q", rec.Keywords, k)
		}
	}
	if rec.Summary != "use jittered backoff" || !strings.HasPrefix(rec.ID, "mem-") || len(rec.ID) != 16 {
		t.Fatalf("bad record %+v", rec)
	}
	got, err := Search(root, "retry backoff", 5, 0, false)
	if err != nil || len(got) != 1 || got[0].Score <= 0 || got[0].ID != rec.ID {
		t.Fatalf("search = %+v, %v", got, err)
	}
	if got, _ := Search(root, "unrelated", 5, 0, false); len(got) != 0 {
		t.Fatalf("non-matching query returned %+v", got)
	}
}

func TestAddRejectsType(t *testing.T) {
	_, err := Add(t.TempDir(), "guess", "k", "s", "", nil, 0.5)
	if err == nil || err.Error() != "unsupported memory type: guess" {
		t.Fatalf("err = %v", err)
	}
}

func TestAddRejectsEscapingFile(t *testing.T) {
	_, err := Add(t.TempDir(), "decision", "k", "s", "", []string{"../outside.txt"}, 0.5)
	if err == nil || !strings.Contains(err.Error(), "memory file path must stay within workspace") {
		t.Fatalf("err = %v", err)
	}
}

func TestStaleAfterEdit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\n")
	if _, err := Add(root, "verified-fix", "alpha", "fix alpha", "", []string{"a.go"}, 0.8); err != nil {
		t.Fatal(err)
	}
	if got, _ := List(root); len(got) != 1 || got[0].Stale {
		t.Fatalf("fresh record = %+v", got)
	}
	writeFile(t, root, "a.go", "package a // changed\n")
	if got, _ := List(root); !got[0].Stale {
		t.Fatal("edited file must mark record stale")
	}
	if got, _ := Search(root, "alpha", 5, 0, true); len(got) != 0 {
		t.Fatal("excludeStale must hide stale record")
	}
	kept, pruned, err := Prune(root)
	if err != nil || kept != 0 || pruned != 1 {
		t.Fatalf("prune = %d %d %v", kept, pruned, err)
	}
}

func TestConfidenceClamp(t *testing.T) {
	root := t.TempDir()
	hi, _ := Add(root, "pattern", "k", "s", "", nil, 1.7)
	lo, _ := Add(root, "pattern", "k", "s", "", nil, -1)
	if hi.Confidence != 1 || lo.Confidence != 0 {
		t.Fatalf("clamp = %v %v", hi.Confidence, lo.Confidence)
	}
}

func TestImportDedupe(t *testing.T) {
	src := t.TempDir()
	for _, s := range []string{"one", "two"} {
		if _, err := Add(src, "decision", s, s, "", nil, 0.7); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "export.jsonl")
	if n, err := Export(src, out); err != nil || n != 2 {
		t.Fatalf("export = %d %v", n, err)
	}
	dst := t.TempDir()
	if imp, skip, bad, err := Import(dst, out); err != nil || imp != 2 || skip != 0 || bad != 0 {
		t.Fatalf("first import = %d %d %d %v", imp, skip, bad, err)
	}
	if imp, skip, _, _ := Import(dst, out); imp != 0 || skip != 2 {
		t.Fatalf("second import = %d %d", imp, skip)
	}
	badFile := filepath.Join(t.TempDir(), "bad.jsonl")
	writeFile(t, filepath.Dir(badFile), "bad.jsonl", "{\"id\":\"\"}\n")
	if _, _, bad, _ := Import(dst, badFile); bad != 1 {
		t.Fatalf("invalid = %d", bad)
	}
}

func TestCorruptLine(t *testing.T) {
	root := t.TempDir()
	if _, err := Add(root, "decision", "k", "s", "", nil, 0.5); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(Path(root), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("not json\n")
	f.Close()
	if _, err := List(root); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v", err)
	}
}
