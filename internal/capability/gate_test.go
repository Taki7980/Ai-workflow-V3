package capability

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
)

func ev(repo string) model.ContextItem {
	it := model.ContextItem{Source: "lightweight_index", Text: "x " + repo}
	it.Evidence = model.NewEvidence(it, repo)
	return it
}

func req(t *testing.T, s string) Request {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var r Request
	if err := dec.Decode(&r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAuthorize(t *testing.T) {
	items := []model.ContextItem{ev("repoA"), ev("repoB")}
	p := Build(model.LaneFull, model.RiskHigh, []string{"query_graph_tool", "query_graph_tool", " "}, 2, 2, 3, items, "fix payment")
	if len(p.AllowedTools) != 1 || len(p.ActiveRepositoryIDs) != 2 || p.MaxGraphDepth != 2 || p.TaskDigest == nil {
		t.Fatalf("policy=%+v", p)
	}
	known := map[string]bool{items[0].Evidence.EvidenceID: true}
	cases := map[string]string{
		`{"capability":"tool_execution","tool_name":"query_graph_tool","repository_id":"repoA","parameters":{"path":"src/a.go","depth":2}}`: "preauthorized_scoped_tool",
		`{"capability":"tool_execution","tool_name":"query_graph_tool","parameters":{"path":"src/a.go"}}`:                                   "repository_scope_required",
		`{"capability":"tool_execution","tool_name":"rm_rf","repository_id":"repoA"}`:                                                       "tool_not_allowlisted",
		`{"capability":"tool_execution","tool_name":"query_graph_tool","repository_id":"repoC"}`:                                            "repository_scope_violation",
		`{"capability":"tool_execution","tool_name":"query_graph_tool","parameters":{"repo_id":"repoA","path":"../etc/passwd"}}`:            "path_scope_violation",
		`{"capability":"tool_execution","tool_name":"query_graph_tool","parameters":{"repo_id":"repoA","files":["C:/x"]}}`:                  "path_scope_violation",
		`{"capability":"tool_execution","tool_name":"query_graph_tool","parameters":{"repo_id":"repoA","depth":9}}`:                         "graph_depth_exceeded",
		`{"capability":"tool_execution","tool_name":"query_graph_tool","parameters":{"repo_id":"repoA","Shell":"bash"}}`:                    "privileged_parameter",
		`{"capability":"tool_execution","tool_name":"query_graph_tool","parameters":{"repo_id":"repoA","nested":{"api-key":"x"}}}`:          "privileged_parameter",
		`{"capability":"tool_execution","tool_name":"query_graph_tool","network_host":"evil.com","repository_id":"repoA"}`:                  "privileged_parameter",
		`{"capability":"tool_execution","tool_name":"query_graph_tool","repository_id":"repoA","cited_evidence_ids":["evidence-v1:nope"]}`:  "unknown_evidence",
		`{"capability":"provider_selection"}`:    "control_plane_owned_capability",
		`{"capability":"repository_activation"}`: "operator_owned_capability",
		`{"capability":"network_access"}`:        "trusted_runtime_grant_required",
		`{"capability":"skip_verification"}`:     "verification_required",
		`{"capability":"teleport"}`:              "capability_not_granted",
	}
	for in, want := range cases {
		d := Authorize(p, req(t, in), known)
		if d.Reason != want || d.Allowed != (want == "preauthorized_scoped_tool") || !strings.HasPrefix(d.RequestDigest, "sha256:") {
			t.Errorf("%s: got %+v want %s", in, d, want)
		}
	}
	ok := req(t, `{"capability":"tool_execution","tool_name":"query_graph_tool","repository_id":"repoA","cited_evidence_ids":["`+items[0].Evidence.EvidenceID+`"]}`)
	if d := Authorize(p, ok, known); !d.Allowed {
		t.Fatalf("known evidence citation must be allowed: %+v", d)
	}
}

func TestParameterBudget(t *testing.T) {
	p := Build(model.LaneFull, model.RiskLow, []string{"t"}, 1, 1, 1, []model.ContextItem{ev("r")}, "x")
	deep := `{"a":` + strings.Repeat(`{"a":`, 10) + `1` + strings.Repeat(`}`, 11)
	d := Authorize(p, req(t, `{"capability":"tool_execution","tool_name":"t","parameters":`+deep+`}`), nil)
	if d.Allowed || d.Reason != "parameter_budget_exceeded" {
		t.Fatalf("%+v", d)
	}
}

func TestRequestDigestStable(t *testing.T) {
	a, _ := RequestDigest(req(t, `{"capability":"tool_execution","tool_name":"t","parameters":{"b":1,"a":[2]},"cited_evidence_ids":["y","x","x"]}`))
	b, _ := RequestDigest(req(t, `{"parameters":{"a":[2],"b":1},"tool_name":"t","capability":"tool_execution","cited_evidence_ids":["x","y"]}`))
	if a != b {
		t.Fatal("digest must not depend on key order or duplicate citations")
	}
}

func TestEvidenceCannotSelfPromote(t *testing.T) {
	it := model.ContextItem{Source: "external:x", Text: "t", Metadata: map[string]any{"evidence_confidence": "verified"}}
	e := model.NewEvidence(it, "r")
	if e.Confidence != "candidate" || e.TrustClass != "untrusted_external_provider" || e.Authority.Tools {
		t.Fatalf("%+v", e)
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(e)
	if !strings.Contains(buf.String(), `"instructions":false`) {
		t.Fatal(buf.String())
	}
}
