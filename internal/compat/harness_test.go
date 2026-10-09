package compat

import "testing"

func TestFrozenV2Contracts(t *testing.T) {
	report, err := Run("../../compat/cases.json", "../../compat/fixtures/v2-contracts.json", "../../compat/v2.lock")
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("V2/V3 compatibility drift: %#v", report.Mismatches)
	}
	if report.Cases != 99 {
		t.Fatalf("compatibility cases=%d want=99 (including MMR, workflow and structural contracts)", report.Cases)
	}
}

func TestHarnessCoversWorkflowSections(t *testing.T) {
	var cases Cases
	if err := readJSON("../../compat/cases.json", &cases); err != nil {
		t.Fatal(err)
	}
	w := cases.WorkflowCases
	for name, n := range map[string]int{
		"orchestration": len(w.Orchestration), "sufficiency": len(w.Sufficiency), "selective": len(w.Selective),
		"evidence_state": len(w.EvidenceState), "handoff_validate": len(w.HandoffValidate), "handoff_render": len(w.HandoffRender),
		"compress": len(w.Compress), "brief_format": len(w.BriefFormat), "model_tier": len(w.ModelTier),
	} {
		if n == 0 {
			t.Fatalf("section %s has no cases", name)
		}
	}
}

func TestHarnessCoversStructuralSections(t *testing.T) {
	var cases Cases
	if err := readJSON("../../compat/cases.json", &cases); err != nil {
		t.Fatal(err)
	}
	s := cases.StructuralCases
	for name, n := range map[string]int{
		"crg_compact": len(s.CRGCompact), "crg_item": len(s.CRGItem), "crg_verified_empty": len(s.CRGVerifiedEmpty),
		"scip_items": len(s.ScipItems), "repo_key": len(s.RepoKey),
	} {
		if n == 0 {
			t.Fatalf("section %s has no cases", name)
		}
	}
}
