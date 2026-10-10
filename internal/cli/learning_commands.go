package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/learning"
	"github.com/Taki7980/ai-workflow-v3/internal/storage"
)

// loadExact decodes a JSON object keeping numbers as written (signature-stable).
func loadExact(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, fmt.Errorf("%s: input report must be a JSON object", path)
	}
	return m, nil
}

// optionalFloat is a flag that records whether it was set.
type optionalFloat struct{ v *float64 }

func (o *optionalFloat) String() string {
	if o.v == nil {
		return ""
	}
	return fmt.Sprint(*o.v)
}

func (o *optionalFloat) Set(s string) error {
	var f float64
	if _, err := fmt.Sscanf(s, "%g", &f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("invalid number %q", s)
	}
	o.v = &f
	return nil
}

func statFlags(fs *flag.FlagSet) (conf *float64, resamples *int, seed *int64, output *string) {
	return fs.Float64("confidence", .95, "confidence level"), fs.Int("resamples", 5000, "bootstrap resamples"),
		fs.Int64("seed", 20260911, "bootstrap random seed"), fs.String("output", "", "write the result to this file")
}

// learningCmd implements `learning status|record-outcome|evaluate|contextual-policy|create-manifest|verify-manifest|shadow-evaluate`.
func learningCmd(root string, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "learning requires status, record-outcome, evaluate, contextual-policy, create-manifest, verify-manifest, or shadow-evaluate")
		return 2
	}
	sub, args := args[0], args[1:]
	fs := newFlags("learning "+sub, errOut)
	fail := func(err error) int {
		fmt.Fprintln(errOut, err)
		return 1
	}
	cfg, err := config.Load(root)
	if err != nil {
		return fail(err)
	}
	switch sub {
	case "status":
		if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 {
			return 2
		}
		printJSON(out, learning.Status(root, cfg.Context.Learning))
	case "record-outcome":
		success := fs.Bool("success", false, "the verified outcome succeeded")
		failure := fs.Bool("failure", false, "the verified outcome failed")
		source := fs.String("source", "", "trusted verifier source ID (required)")
		verifier := fs.String("verifier-identity", "", "verifier identity (required)")
		digest := fs.String("evidence-digest", "", "sha256:<hex> of the immutable evidence (required)")
		var reward optionalFloat
		fs.Var(&reward, "reward", "reward in [0,1] (default 1 for success, 0 for failure)")
		cost := fs.Float64("realized-cost", 0, "non-negative realized cost")
		pos, err := parseInterspersed(fs, args)
		if err != nil || len(pos) != 1 || *success == *failure || *source == "" || *verifier == "" || *digest == "" {
			fmt.Fprintln(errOut, "record-outcome requires DECISION_ID, exactly one of --success/--failure, --source, --verifier-identity, --evidence-digest")
			return 2
		}
		path, err := learning.RecordOutcome(root, pos[0], learning.Outcome{Success: *success, Source: *source, VerifierIdentity: *verifier,
			EvidenceDigest: *digest, Reward: reward.v, RealizedCost: *cost})
		if err != nil {
			return fail(err)
		}
		rel, _ := filepath.Rel(root, path)
		printJSON(out, map[string]any{"recorded": true, "decision_id": pos[0], "path": filepath.ToSlash(rel)})
	case "evaluate", "contextual-policy":
		var arms, fields stringList
		fs.Var(&arms, "arm", "target arm (repeatable)")
		fs.Var(&fields, "field", "context feature field (repeatable)")
		conf, resamples, seed, output := statFlags(fs)
		minESS := fs.Float64("minimum-effective-sample-size", 10, "minimum effective sample size")
		minDirect := fs.Int("minimum-direct-exposures", 5, "minimum direct target exposures")
		margin := fs.Float64("safety-margin", 0, "required reward lower-bound improvement")
		var maxCost optionalFloat
		fs.Var(&maxCost, "max-realized-cost", "maximum allowed direct realized cost")
		devFraction := fs.Float64("development-fraction", .7, "development split fraction")
		prior := fs.Float64("prior-weight", 5, "direct-model smoothing prior weight")
		folds := fs.Int("folds", 5, "cross-validation folds")
		minContext := fs.Int("minimum-context-events", 10, "minimum development events per context")
		minHoldout := fs.Int("minimum-holdout-events", 20, "minimum holdout events")
		minValidation := fs.Int("minimum-model-validation-events", 10, "minimum reward-model validation events")
		minGain := fs.Float64("minimum-estimated-gain", 0, "minimum direct-model gain over baseline")
		if sub == "contextual-policy" {
			fs.Lookup("minimum-direct-exposures").DefValue = "3"
			*minDirect = 3
		}
		if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 {
			return 2
		}
		var r map[string]any
		if sub == "evaluate" {
			r, err = learning.Evaluate(root, learning.EvaluateOptions{Arms: arms, Confidence: *conf, Resamples: *resamples, Seed: *seed,
				MinimumESS: *minESS, MinimumDirect: *minDirect, SafetyMargin: *margin, MaxRealizedCost: maxCost.v})
		} else {
			r, err = learning.ContextualPolicy(root, learning.ContextualOptions{Fields: fields, DevelopmentFraction: *devFraction, PriorWeight: *prior,
				Folds: *folds, MinContext: *minContext, MinDirect: *minDirect, MinHoldout: *minHoldout, MinESS: *minESS, MinValidation: *minValidation,
				MinGain: *minGain, SafetyMargin: *margin, MaxRealizedCost: maxCost.v, Confidence: *conf, Resamples: *resamples, Seed: *seed})
		}
		if err != nil {
			return fail(err)
		}
		if *output != "" {
			if err := storage.WriteJSON(*output, r); err != nil {
				return fail(err)
			}
		}
		printJSON(out, r)
	case "create-manifest", "verify-manifest":
		input := fs.String("input", "", "contextual policy report (create) or manifest (verify)")
		output := fs.String("output", "", "manifest output path (create)")
		keyEnv := fs.String("signing-key-env", "", "environment variable holding the HMAC key (required)")
		if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 || *input == "" || *keyEnv == "" || (sub == "create-manifest" && *output == "") {
			fmt.Fprintf(errOut, "%s requires --input, --signing-key-env%s\n", sub, map[bool]string{true: ", --output", false: ""}[sub == "create-manifest"])
			return 2
		}
		doc, err := loadExact(*input)
		if err != nil {
			return fail(err)
		}
		key, err := learning.SigningKey(*keyEnv)
		if err != nil {
			return fail(err)
		}
		if sub == "verify-manifest" {
			r := learning.VerifyManifest(doc, key)
			printJSON(out, r)
			if r["valid"] != true {
				return 1
			}
			return 0
		}
		m, err := learning.CreateManifest(doc, key)
		if err != nil {
			return fail(err)
		}
		if err := storage.WriteJSON(*output, m); err != nil {
			return fail(err)
		}
		abs, _ := filepath.Abs(*output)
		printJSON(out, map[string]any{"created": true, "policy_id": m["policy_id"], "status": m["status"], "output": abs})
	case "shadow-evaluate":
		manifestPath := fs.String("manifest", "", "signed policy manifest (required)")
		keyEnv := fs.String("signing-key-env", "", "environment variable holding the HMAC key (required)")
		conf, resamples, seed, output := statFlags(fs)
		rmin := fs.Float64("reward-min", 0, "minimum reward")
		rmax := fs.Float64("reward-max", 1, "maximum reward")
		maxW := fs.Float64("max-importance-weight", 20, "maximum importance weight")
		minNew := fs.Int("minimum-new-events", 20, "minimum post-cutoff events")
		margin := fs.Float64("safety-margin", 0, "required anytime lower bound")
		var maxCost optionalFloat
		fs.Var(&maxCost, "max-realized-cost", "maximum allowed direct realized cost")
		if pos, err := parseInterspersed(fs, args); err != nil || len(pos) != 0 || *manifestPath == "" || *keyEnv == "" {
			fmt.Fprintln(errOut, "shadow-evaluate requires --manifest and --signing-key-env")
			return 2
		}
		m, err := loadExact(*manifestPath)
		if err != nil {
			return fail(err)
		}
		key, err := learning.SigningKey(*keyEnv)
		if err != nil {
			return fail(err)
		}
		r, err := learning.Shadow(root, m, key, learning.ShadowOptions{Confidence: *conf, RewardMin: *rmin, RewardMax: *rmax, MaxWeight: *maxW,
			SafetyMargin: *margin, MinimumNewEvents: *minNew, Resamples: *resamples, Seed: *seed, MaxRealizedCost: maxCost.v})
		if err != nil {
			return fail(err)
		}
		if *output != "" {
			if err := storage.WriteJSON(*output, r); err != nil {
				return fail(err)
			}
		}
		printJSON(out, r)
	default:
		fmt.Fprintf(errOut, "unknown learning command %q\n", sub)
		return 2
	}
	return 0
}
