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
}
