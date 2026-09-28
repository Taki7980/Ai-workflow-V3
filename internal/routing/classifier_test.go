package routing

import (
	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"testing"
)

func TestClassifierParityCases(t *testing.T) {
	cfg := config.Default()
	cases := []struct {
		task string
		lane model.Lane
		risk model.Risk
	}{
		{"explain how routing works", model.LaneAnswer, model.RiskLow},
		{"fix payment validation", model.LaneFull, model.RiskHigh},
		{"refactor the architecture", model.LaneFull, model.RiskMedium},
		{"rename typo in README.md", model.LaneSmall, model.RiskLow},
		{"do something useful", model.LaneFull, model.RiskMedium},
	}
	for _, c := range cases {
		d := Classify(c.task, c.cfg)
		if d.Lane != c.lane || d.Risk != c.risk {
			t.Fatalf("%q => %s/%s want %s/%s", c.task, d.Lane, d.Risk, c.lane, c.risk)
		}
	}
}
