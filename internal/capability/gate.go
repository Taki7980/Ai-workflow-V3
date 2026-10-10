// Package capability is the deterministic post-model action gate (V2
// capability-v2). Policy is built only from control-plane state; retrieved
// text, tool output and model reasoning can never widen authority.
package capability

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
)

const (
	Schema           = "capability-v2"
	maxParamDepth    = 6
	maxParamNodes    = 256
	maxParamString   = 4096
	maxParamKeyChars = 128
)

// Capabilities a post-model action may request.
const (
	ToolExecution        = "tool_execution"
	ProviderSelection    = "provider_selection"
	RepositoryActivation = "repository_activation"
	NetworkAccess        = "network_access"
	SecretAccess         = "secret_access"
	SafetyLaneChange     = "safety_lane_change"
	SkipVerification     = "skip_verification"
)

var privilegedKeys = set("command", "commands", "shell", "argv", "executable", "network_host", "host", "hostname", "url", "uri",
	"endpoint_url", "secret", "secret_name", "secrets", "token", "api_key", "credential", "credentials", "provider", "provider_id",
	"requested_lane", "lane", "risk", "skip_verification", "verification_passes", "repository_activation", "activate_repository")
var repositoryKeys = set("repository_id", "repo_id")
var depthKeys = set("depth", "graph_depth", "max_depth")
var pathKeys = set("path", "file", "files", "changed_file", "changed_files", "source_path", "target_path")

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// Policy is fixed before the model runs.
type Policy struct {
	Schema              string   `json:"schema"`
	Lane                string   `json:"lane"`
	Risk                string   `json:"risk"`
	AllowedTools        []string `json:"allowed_tools"`
	ActiveRepositoryIDs []string `json:"active_repository_ids"`
	VerificationPasses  int      `json:"verification_passes"`
	MaxGraphDepth       int      `json:"max_graph_depth"`
	DeniedByDefault     []string `json:"denied_by_default"`
	TaskDigest          *string  `json:"task_digest"`
	PolicyOwner         string   `json:"policy_owner"`
}

// Request is one model-proposed action.
type Request struct {
	Capability       string         `json:"capability"`
	ToolName         string         `json:"tool_name,omitempty"`
	ProviderID       string         `json:"provider_id,omitempty"`
	RepositoryID     *string        `json:"repository_id,omitempty"`
	NetworkHost      string         `json:"network_host,omitempty"`
	SecretName       string         `json:"secret_name,omitempty"`
	RequestedLane    string         `json:"requested_lane,omitempty"`
	Parameters       map[string]any `json:"parameters,omitempty"`
	CitedEvidenceIDs []string       `json:"cited_evidence_ids,omitempty"`
}

// Decision is the bound result for one exact request.
type Decision struct {
	Allowed       bool    `json:"allowed"`
	Reason        string  `json:"reason"`
	Capability    string  `json:"capability"`
	RequestDigest string  `json:"request_digest"`
	PolicySchema  string  `json:"policy_schema"`
	TaskDigest    *string `json:"task_digest"`
}

// TaskDigest binds policy to the task without persisting its text.
func TaskDigest(task string) *string {
	sum := sha256.Sum256([]byte("task-scope-v1\x00" + strings.Join(strings.Fields(task), " ")))
	s := "sha256:" + hex.EncodeToString(sum[:])
	return &s
}

// Build derives policy from routing, the orchestration CRG plan and the
// system-assigned repository identities of the evidence.
func Build(lane model.Lane, risk model.Risk, crgPlan []string, verificationPasses, graphDepth, budgetDepth int, evidence []model.ContextItem, task string) Policy {
	tools := []string{}
	for _, t := range crgPlan {
		if t = strings.TrimSpace(t); t != "" && !slices.Contains(tools, t) {
			tools = append(tools, t)
		}
	}
	repos := []string{}
	for _, it := range evidence {
		if it.Evidence != nil && it.Evidence.RepositoryID != "" && !slices.Contains(repos, it.Evidence.RepositoryID) {
			repos = append(repos, it.Evidence.RepositoryID)
		}
	}
	sort.Strings(repos)
	if budgetDepth <= 0 {
		budgetDepth = graphDepth
	}
	return Policy{
		Schema: Schema, Lane: string(lane), Risk: string(risk), AllowedTools: tools, ActiveRepositoryIDs: repos,
		VerificationPasses: max(1, verificationPasses), MaxGraphDepth: max(1, min(graphDepth, budgetDepth)),
		DeniedByDefault: []string{ProviderSelection, RepositoryActivation, NetworkAccess, SecretAccess, SafetyLaneChange, SkipVerification,
			"tool_execution_unless_allowlisted_and_scoped", "privileged_parameters_from_model", "repository_scope_expansion", "absolute_or_traversal_paths"},
		TaskDigest: TaskDigest(task), PolicyOwner: "control_plane",
	}
}

var errBudget = errors.New("action parameters exceed structural budget")

// canonical validates parameter structure and returns a JSON-safe copy.
func canonical(v any, depth int, nodes *int) (any, error) {
	*nodes++
	if *nodes > maxParamNodes || depth > maxParamDepth {
		return nil, errBudget
	}
	switch x := v.(type) {
	case nil, bool, json.Number:
		return x, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, errors.New("action parameters must contain finite numbers")
		}
		return x, nil
	case string:
		if len(x) > maxParamString {
			return nil, errBudget
		}
		return x, nil
	case map[string]any:
		if len(x) > maxParamNodes {
			return nil, errBudget
		}
		out := map[string]any{}
		for k, vv := range x {
			if k == "" || len(k) > maxParamKeyChars {
				return nil, errors.New("action parameter keys must be bounded non-empty strings")
			}
			c, err := canonical(vv, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	case []any:
		if len(x) > maxParamNodes {
			return nil, errBudget
		}
		out := make([]any, 0, len(x))
		for _, vv := range x {
			c, err := canonical(vv, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, nil
	}
	return nil, errors.New("unsupported action parameter type")
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// RequestDigest hashes the exact request (model-action-v2).
func RequestDigest(r Request) (string, error) {
	nodes := 0
	params, err := canonical(orMap(r.Parameters), 0, &nodes)
	if err != nil {
		return "", err
	}
	cited := append([]string{}, r.CitedEvidenceIDs...)
	sort.Strings(cited)
	cited = slices.Compact(cited)
	var repo any
	if r.RepositoryID != nil {
		repo = *r.RepositoryID
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{"schema": "model-action-v2", "capability": r.Capability, "tool_name": nullable(r.ToolName),
		"provider_id": nullable(r.ProviderID), "repository_id": repo, "network_host": nullable(r.NetworkHost),
		"secret_name": nullable(r.SecretName), "requested_lane": nullable(r.RequestedLane), "parameters": params, "cited_evidence_ids": cited}); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func orMap(m map[string]any) any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

var drivePrefix = regexp.MustCompile(`^[A-Za-z]:`)

func safeRelativePath(v string) bool {
	raw := strings.ReplaceAll(strings.TrimSpace(v), `\`, "/")
	if raw == "" || strings.ContainsRune(raw, 0) || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "~") ||
		strings.Contains(raw, "://") || drivePrefix.MatchString(raw) {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(raw, "/"), "/") {
		if part == ".." || part == "" {
			return false
		}
	}
	return true
}

func pathValuesSafe(v any) bool {
	switch x := v.(type) {
	case string:
		return safeRelativePath(x)
	case []any:
		if len(x) == 0 {
			return false
		}
		for _, it := range x {
			s, ok := it.(string)
			if !ok || !safeRelativePath(s) {
				return false
			}
		}
		return true
	}
	return false
}

func isPathKey(k string) bool {
	return pathKeys[k] || strings.HasSuffix(k, "_path") || strings.HasSuffix(k, "_file") || strings.HasSuffix(k, "_files")
}

// asInt accepts only JSON integers (requests are decoded with UseNumber).
func asInt(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil
}

// validateParameters walks tool parameters, returning a denial reason and the
// repository IDs they reference.
func validateParameters(p Policy, params map[string]any) (string, []string) {
	repos := []string{}
	nodes := 0
	var walk func(v any, key string, hasKey bool, depth int) string
	walk = func(v any, key string, hasKey bool, depth int) string {
		nodes++
		if nodes > maxParamNodes || depth > maxParamDepth {
			return "parameter_budget_exceeded"
		}
		if hasKey {
			k := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
			if privilegedKeys[k] {
				return "privileged_parameter"
			}
			if repositoryKeys[k] {
				s, ok := v.(string)
				if !ok || strings.TrimSpace(s) == "" {
					return "invalid_request"
				}
				if !slices.Contains(p.ActiveRepositoryIDs, strings.TrimSpace(s)) {
					return "repository_scope_violation"
				}
				repos = append(repos, strings.TrimSpace(s))
			}
			if depthKeys[k] {
				n, ok := asInt(v)
				if !ok {
					return "invalid_request"
				}
				if n < 0 || n > int64(p.MaxGraphDepth) {
					return "graph_depth_exceeded"
				}
			}
			if isPathKey(k) && !pathValuesSafe(v) {
				return "path_scope_violation"
			}
		}
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				if k == "" {
					return "invalid_request"
				}
				if r := walk(vv, k, true, depth+1); r != "" {
					return r
				}
			}
		case []any:
			for _, vv := range x {
				if r := walk(vv, "", false, depth+1); r != "" {
					return r
				}
			}
		case string:
			if len(x) > maxParamString {
				return "parameter_budget_exceeded"
			}
		case nil, bool, json.Number:
		case float64:
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return "invalid_request"
			}
		default:
			return "invalid_request"
		}
		return ""
	}
	return walk(orMap(params), "", false, 0), repos
}

// Authorize decides one request against p using only known evidence IDs.
func Authorize(p Policy, r Request, knownEvidence map[string]bool) Decision {
	d := Decision{Capability: r.Capability, PolicySchema: p.Schema, TaskDigest: p.TaskDigest}
	deny := func(reason string) Decision { d.Reason = reason; return d }
	digest, err := RequestDigest(r)
	if err != nil {
		d.RequestDigest = "sha256:" + strings.Repeat("0", 64)
		if errors.Is(err, errBudget) {
			return deny("parameter_budget_exceeded")
		}
		return deny("invalid_request")
	}
	d.RequestDigest = digest
	for _, id := range r.CitedEvidenceIDs {
		if !knownEvidence[id] {
			return deny("unknown_evidence")
		}
	}
	switch r.Capability {
	case ToolExecution:
		tool := strings.TrimSpace(r.ToolName)
		if tool == "" {
			return deny("invalid_request")
		}
		if !slices.Contains(p.AllowedTools, tool) {
			return deny("tool_not_allowlisted")
		}
		for _, v := range []string{r.ProviderID, r.NetworkHost, r.SecretName, r.RequestedLane} {
			if strings.TrimSpace(v) != "" {
				return deny("privileged_parameter")
			}
		}
		refs := []string{}
		if r.RepositoryID != nil {
			id := strings.TrimSpace(*r.RepositoryID)
			if id == "" || !slices.Contains(p.ActiveRepositoryIDs, id) {
				return deny("repository_scope_violation")
			}
			refs = append(refs, id)
		}
		reason, prefs := validateParameters(p, r.Parameters)
		if reason != "" {
			return deny(reason)
		}
		if refs = append(refs, prefs...); len(p.ActiveRepositoryIDs) > 1 && len(refs) == 0 {
			return deny("repository_scope_required")
		}
		d.Allowed, d.Reason = true, "preauthorized_scoped_tool"
		return d
	case ProviderSelection, SafetyLaneChange:
		return deny("control_plane_owned_capability")
	case RepositoryActivation:
		return deny("operator_owned_capability")
	case NetworkAccess, SecretAccess:
		return deny("trusted_runtime_grant_required")
	case SkipVerification:
		return deny("verification_required")
	}
	return deny("capability_not_granted")
}
