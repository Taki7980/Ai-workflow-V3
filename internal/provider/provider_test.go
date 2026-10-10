package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a provider: PROVIDER_HELPER selects behaviour.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == ExecWrapperCommand { // sandbox rlimit wrapper
		os.Exit(ExecWithLimits(os.Args[2:], os.Stderr))
	}
	switch os.Getenv("PROVIDER_HELPER") {
	case "items":
		in, _ := io.ReadAll(os.Stdin)
		var req map[string]any
		_ = json.Unmarshal(in, &req)
		fmt.Printf(`{"items":[{"text":"hit for %v","score":0.9,"path":"src/a.go","line":3},`+
			`{"text":"escape","score":0.5,"path":"../../etc/passwd"},{"text":"abs","score":0.4,"path":"/etc/passwd"},`+
			`{"text":"drive","score":0.3,"file":"C:\\Windows\\x"}]}`, req["query"])
		os.Exit(0)
	case "env":
		b, _ := json.Marshal([]map[string]string{{"text": "secret=" + os.Getenv("PROVIDER_SECRET") + " home=" + os.Getenv("HOME")}})
		os.Stdout.Write(b)
		os.Exit(0)
	case "stderr":
		fmt.Fprint(os.Stderr, "Authorization: Bearer abc.def token=xyz value "+os.Getenv("PROVIDER_SECRET"))
		os.Exit(4)
	case "sleep":
		time.Sleep(time.Hour)
	case "flood":
		for {
			os.Stdout.Write([]byte(strings.Repeat("y", 8192)))
		}
	case "badscore":
		fmt.Print(`[{"text":"x","score":7}]`)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func helperSpec(t *testing.T, role string) Spec {
	t.Helper()
	t.Setenv("PROVIDER_HELPER", role)
	return Spec{Name: "helper", Command: []string{os.Args[0]}, EnvAllowlist: []string{"PROVIDER_HELPER"}}
}

func req(root string) Request { return Request{Query: "RetryPayment", Root: root, Limit: 10} }

func TestRunConfinesPathsAndTagsTrust(t *testing.T) {
	root := t.TempDir()
	r := Run(context.Background(), helperSpec(t, "items"), req(root), "external")
	if !r.OK() || len(r.Items) != 4 {
		t.Fatalf("r=%+v", r)
	}
	if r.Items[0].Metadata["path"] != "src/a.go" || r.Items[0].Text != "hit for RetryPayment" {
		t.Fatalf("first=%+v", r.Items[0])
	}
	for _, it := range r.Items[1:] {
		if _, ok := it.Metadata["path"]; ok || it.Metadata["path_rejected"] != true {
			t.Fatalf("unsafe path kept: %+v", it.Metadata)
		}
		if _, ok := it.Metadata["file"]; ok {
			t.Fatalf("unsafe file kept: %+v", it.Metadata)
		}
		if it.Metadata["trust"] != "untrusted_repository_content" {
			t.Fatal("missing trust tag")
		}
	}
}

func TestEnvProfiles(t *testing.T) {
	t.Setenv("PROVIDER_SECRET", "s3cr3t")
	root := t.TempDir()
	r := Run(context.Background(), helperSpec(t, "env"), req(root), "x")
	if !r.OK() || strings.Contains(r.Items[0].Text, "s3cr3t") {
		t.Fatalf("compatibility profile leaked a non-allowlisted secret: %+v", r)
	}
	s := helperSpec(t, "env")
	s.RuntimeProfile = "restricted"
	r = Run(context.Background(), s, req(root), "x")
	if !r.OK() || strings.Contains(r.Items[0].Text, "secret=s3cr3t") {
		t.Fatalf("r=%+v", r)
	}
	if runtime.GOOS != "windows" && !strings.Contains(r.Items[0].Text, "ai-workflow-provider-") {
		t.Fatalf("restricted HOME must be an isolated temp dir: %s", r.Items[0].Text)
	}
	s.EnvAllowlist = append(s.EnvAllowlist, "PATH")
	if err := s.Validate(); err == nil {
		t.Fatal("restricted profile must refuse overriding PATH")
	}
}

func TestFailureKinds(t *testing.T) {
	root := t.TempDir()
	s := helperSpec(t, "stderr")
	t.Setenv("PROVIDER_SECRET", "hunter2")
	s.EnvAllowlist = append(s.EnvAllowlist, "PROVIDER_SECRET")
	r := Run(context.Background(), s, req(root), "x")
	if r.ErrorKind != "exit" || *r.ReturnCode != 4 {
		t.Fatalf("r=%+v", r)
	}
	for _, leak := range []string{"abc.def", "xyz", "hunter2"} {
		if strings.Contains(r.StderrTail, leak) {
			t.Fatalf("stderr not redacted: %q", r.StderrTail)
		}
	}

	s = helperSpec(t, "sleep")
	s.Timeout = time.Second
	if r := Run(context.Background(), s, req(root), "x"); r.ErrorKind != "timeout" || !r.TimedOut {
		t.Fatalf("r=%+v", r)
	}
	s = helperSpec(t, "flood")
	s.MaxOutputBytes = 1000
	if r := Run(context.Background(), s, req(root), "x"); r.ErrorKind != "output_limit" || !r.OutputLimited {
		t.Fatalf("r=%+v", r)
	}
	if r := Run(context.Background(), helperSpec(t, "badscore"), req(root), "x"); r.ErrorKind != "invalid_payload" {
		t.Fatalf("r=%+v", r)
	}
}

func TestStrictJSON(t *testing.T) {
	bad := map[string]string{
		"dup":      `[{"text":"a","text":"b"}]`,
		"bigint":   `[{"text":"a","line":99999999999999999999}]`,
		"trailing": `[{"text":"a"}] x`,
		"deep":     `[{"metadata":{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":1}}}}}}}}}]`,
		"noitems":  `{"results":[]}`,
		"scalar":   `[1]`,
		"emptykey": `[{"":1}]`,
	}
	for name, in := range bad {
		if _, err := parseRecords([]byte(in)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	rows, err := parseRecords([]byte("{\"text\":\"a\"}\n\n{\"text\":\"b\"}\n"))
	if err != nil || len(rows) != 2 {
		t.Fatalf("jsonl rows=%v err=%v", rows, err)
	}
}

func FuzzParseRecords(f *testing.F) {
	for _, s := range []string{`[{"text":"a","score":0.5}]`, `{"items":[]}`, "{\"text\":\"x\"}\n{\"text\":\"y\"}", `[[[[`, `{"a":1,"a":2}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		rows, err := parseRecords(b)
		if err == nil && len(rows) > MaxRecords {
			t.Fatalf("record limit bypassed: %d", len(rows))
		}
		_, _ = contextItems(t.TempDir(), b, "fuzz", 5, Spec{Name: "f", ExecutableTrust: TrustConfigured})
	})
}

func TestSandboxPolicy(t *testing.T) {
	for _, p := range []SandboxPolicy{{Mode: "bogus"}, {Mode: "off", Network: "deny"}, {Mode: "off", Limits: ResourceLimits{CPUSeconds: 1}}, {Mode: "required", Limits: ResourceLimits{MemoryMB: -1}}} {
		if err := p.Validate(); err == nil {
			t.Errorf("%+v accepted", p)
		}
	}
	old := resolveBubblewrap
	resolveBubblewrap = func(string) string { return "" }
	t.Cleanup(func() { resolveBubblewrap = old })
	if _, err := buildSandboxPlan(SandboxPolicy{Mode: "required", Network: "host", Backend: "auto"}, []string{"x"}, ".", nil); err == nil {
		t.Fatal("required sandbox without backend must fail closed")
	}
	plan, err := buildSandboxPlan(SandboxPolicy{Mode: "preferred", Network: "host", Backend: "auto"}, []string{"x"}, ".", nil)
	if err != nil || plan.sandboxed || plan.fallbackReason != "backend_unavailable" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	resolveBubblewrap = func(string) string { return "/usr/bin/bwrap" }
	plan, err = buildSandboxPlan(SandboxPolicy{Mode: "required", Network: "deny", Backend: "auto", Limits: ResourceLimits{CPUSeconds: 5}}, []string{"prov", "--x"}, ".", nil)
	joined := strings.Join(plan.argv, " ")
	if err != nil || !plan.sandboxed || !strings.Contains(joined, "--unshare-net") || !strings.Contains(joined, ExecWrapperCommand+" --cpu-seconds 5 -- prov --x") {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	s := helperSpec(t, "items")
	s.Sandbox = SandboxPolicy{Mode: "required"}
	resolveBubblewrap = func(string) string { return "" }
	if r := Run(context.Background(), s, req(t.TempDir()), "x"); r.ErrorKind != "sandbox_unavailable" {
		t.Fatalf("r=%+v", r)
	}
}

func writeRegistry(t *testing.T, providers map[string]any) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "providers.json")
	b, _ := json.Marshal(map[string]any{"providers": providers})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(RegistryEnv, p)
}

func TestTrustedRegistry(t *testing.T) {
	root := t.TempDir()
	exe, _ := filepath.Abs(os.Args[0])
	digest, err := sha256File(exe)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_HELPER", "items")
	writeRegistry(t, map[string]any{
		"good":   map[string]any{"command": []string{exe}, "sha256": digest, "env_allowlist": []string{"PROVIDER_HELPER"}, "runtime_profile": "compatibility", "timeout_seconds": 30},
		"wrong":  map[string]any{"command": []string{exe}, "sha256": strings.Repeat("0", 64)},
		"rel":    map[string]any{"command": []string{"helper"}, "sha256": digest},
		"script": map[string]any{"command": []string{exe, "evil.py"}, "sha256": digest},
	})
	if err := os.WriteFile(filepath.Join(root, "evil.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ResolveProject(root, json.RawMessage(`{"provider_id":"good","timeout_seconds":5}`), "semantic")
	if err != nil || s.ExecutableTrust != TrustRegistry || s.Timeout != 5*time.Second || !s.NeutralCWD {
		t.Fatalf("s=%+v err=%v", s, err)
	}
	if r := Run(context.Background(), s, req(root), "semantic"); !r.OK() || len(r.Items) == 0 {
		t.Fatalf("trusted run failed: %+v", r)
	}
	for id, want := range map[string]string{"wrong": "digest mismatch", "rel": "absolute", "script": "repository-owned", "missing": "not registered"} {
		if _, err := ResolveProject(root, json.RawMessage(`{"provider_id":"`+id+`"}`), "x"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err=%v", id, err)
		}
	}
	if _, err := ResolveProject(root, json.RawMessage(`{"provider_id":"good","command":["x"],"sandbox":{"mode":"off"}}`), "x"); err == nil || !strings.Contains(err.Error(), "command, sandbox") {
		t.Fatalf("trusted fields from repo config must be refused: %v", err)
	}
	if _, err := ResolveProject(root, json.RawMessage(`{"command":["`+filepath.ToSlash(exe)+`"]}`), "x"); err == nil {
		t.Fatal("inline repository command must be refused by default")
	}
	t.Setenv(UnsafeRepoCommandEnv, "1")
	if _, err := ResolveProject(root, json.RawMessage(`{"command":["`+filepath.ToSlash(exe)+`"]}`), "x"); err != nil {
		t.Fatalf("explicit opt-in must allow inline commands: %v", err)
	}
	// Pinned digest is re-checked at launch.
	s.ExecutableSHA256 = strings.Repeat("1", 64)
	if r := Run(context.Background(), s, req(root), "x"); r.ErrorKind != "provider_trust" {
		t.Fatalf("r=%+v", r)
	}
}

func TestRegistryInsideRepoRefused(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "providers.json")
	if err := os.WriteFile(p, []byte(`{"providers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(RegistryEnv, p)
	if _, err := ResolveProject(root, json.RawMessage(`{"provider_id":"x"}`), "x"); err == nil || !strings.Contains(err.Error(), "outside the repository") {
		t.Fatalf("err=%v", err)
	}
}
