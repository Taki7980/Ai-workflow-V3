// Package journal writes and verifies immutable, hash-chained run journals
// and replays them without external effects (V2 run_journal.py parity).
// Digests detect accidental or partial modification; they are not signatures
// against an attacker who can rewrite the whole file.
package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

const (
	Relative      = "ai-workspace/generated/run-journal"
	SchemaVersion = "ai-workflow-run-journal-v2"
	Mode          = "decision-history-only"
	digestField   = "journal_digest"
	maxJournal    = 8 << 20
	maxChanged    = 4096
	maxPathChars  = 4096
)

// EventKinds is the exact ordered event contract.
var EventKinds = []string{"routing", "repository_routing", "retrieval", "ranking", "selection", "orchestration", "authorization"}

var requiredObjects = []string{"policy_identity", "workspace_state", "graph_state", "repository_state", "provider_versions", "retrieval"}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Event is one journal step before chaining.
type Event struct {
	Kind    string
	Payload any
}

// ValidRunID reports whether id is safe to use as a file name.
func ValidRunID(id string) bool { return safeID.MatchString(id) }

// canonical encodes v like Python json.dumps(sort_keys, compact, ensure_ascii=False).
func canonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func digest(v any) (string, error) {
	b, err := canonical(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// clone round-trips v through JSON into plain maps/slices with json.Number.
func clone(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	return out, dec.Decode(&out)
}

func journalDigest(rec map[string]any) (string, error) {
	body := make(map[string]any, len(rec))
	for k, v := range rec {
		if k != digestField {
			body[k] = v
		}
	}
	return digest(body)
}

func validChanged(rec map[string]any) ([]string, error) {
	raw, ok := rec["changed_files"].([]any)
	if !ok {
		return nil, errors.New("changed_files must be a list")
	}
	if len(raw) > maxChanged {
		return nil, errors.New("changed_files exceeds replay safety bound")
	}
	out := make([]string, 0, len(raw))
	for _, it := range raw {
		s, ok := it.(string)
		if !ok || s == "" || strings.ContainsRune(s, 0) || len(s) > maxPathChars {
			return nil, errors.New("changed_files contains an invalid path")
		}
		out = append(out, s)
	}
	return out, nil
}

// Build validates record, chains events and returns the sealed journal.
func Build(record map[string]any, events []Event) (map[string]any, error) {
	runID, _ := record["run_id"].(string)
	if !ValidRunID(runID) {
		return nil, errors.New("invalid run_id")
	}
	c, err := clone(record)
	if err != nil {
		return nil, err
	}
	rec := c.(map[string]any)
	for _, k := range requiredObjects {
		if _, ok := rec[k].(map[string]any); !ok {
			return nil, fmt.Errorf("%s must be an object", k)
		}
	}
	if _, ok := rec["selected_evidence"].([]any); !ok {
		return nil, errors.New("selected_evidence must be a list")
	}
	if _, err := validChanged(rec); err != nil {
		return nil, err
	}
	if len(events) != len(EventKinds) {
		return nil, errors.New("replay events must match the v2 event contract exactly")
	}
	chained := make([]any, 0, len(events))
	var prev any
	for i, ev := range events {
		if ev.Kind != EventKinds[i] {
			return nil, errors.New("replay events must match the v2 event contract exactly")
		}
		p, err := clone(ev.Payload)
		if err != nil {
			return nil, err
		}
		if _, ok := p.(map[string]any); !ok {
			return nil, errors.New("replay event payload must be an object")
		}
		body := map[string]any{"sequence": i, "kind": ev.Kind, "previous_event_digest": prev, "payload": p}
		d, err := digest(body)
		if err != nil {
			return nil, err
		}
		body["event_digest"] = d
		chained = append(chained, body)
		prev = d
	}
	rec["schema_version"], rec["replay_mode"], rec["run_id"] = SchemaVersion, Mode, runID
	rec["replay_event_count"], rec["replay_head_digest"], rec["replay_events"] = len(chained), prev, chained
	c, err = clone(rec) // normalize ints to json.Number so the digest matches a re-read
	if err != nil {
		return nil, err
	}
	rec = c.(map[string]any)
	d, err := journalDigest(rec)
	if err != nil {
		return nil, err
	}
	rec[digestField] = d
	return rec, nil
}

// Integrity is the verification report for one journal.
type Integrity struct {
	Valid         bool     `json:"valid"`
	Errors        []string `json:"errors"`
	EventCount    int      `json:"event_count"`
	HeadDigest    any      `json:"head_digest"`
	JournalDigest any      `json:"journal_digest"`
}

func intOf(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return int(i), err == nil
}

// Verify checks schema, run ID, event chain, snapshots and journal digest.
func Verify(rec map[string]any, expectedRunID string) Integrity {
	errs := []string{}
	add := func(s string) { errs = append(errs, s) }
	if rec["schema_version"] != SchemaVersion {
		add("schema_version_mismatch")
	}
	if rec["replay_mode"] != Mode {
		add("replay_mode_mismatch")
	}
	recorded, _ := rec["run_id"].(string)
	if !ValidRunID(recorded) {
		recorded = ""
		add("run_id_invalid")
	}
	if expectedRunID != "" {
		if !ValidRunID(expectedRunID) {
			add("expected_run_id_invalid")
		}
		if recorded != expectedRunID {
			add("run_id_mismatch")
		}
	}
	for _, k := range requiredObjects {
		if _, ok := rec[k].(map[string]any); !ok {
			add(k + "_invalid")
		}
	}
	if _, ok := rec["selected_evidence"].([]any); !ok {
		add("selected_evidence_invalid")
	}
	if _, err := validChanged(rec); err != nil {
		add("changed_files_invalid")
	}
	jd := rec[digestField]
	raw, ok := rec["replay_events"].([]any)
	if !ok {
		return Integrity{Errors: append(errs, "replay_events_missing"), JournalDigest: jd}
	}
	if len(raw) != len(EventKinds) {
		add("event_contract_count_mismatch")
	}
	var prev any
	payloads := map[string]any{}
	for i, r := range raw {
		ev, ok := r.(map[string]any)
		if !ok {
			add(fmt.Sprintf("event_%d_invalid", i))
			continue
		}
		if seq, ok := intOf(ev["sequence"]); !ok || seq != i {
			add(fmt.Sprintf("event_%d_sequence_mismatch", i))
		}
		kind, _ := ev["kind"].(string)
		payload, okPayload := ev["payload"].(map[string]any)
		if i < len(EventKinds) && kind != EventKinds[i] {
			add(fmt.Sprintf("event_%d_kind_mismatch", i))
		}
		if kind == "" || !okPayload {
			add(fmt.Sprintf("event_%d_shape_invalid", i))
			continue
		}
		payloads[kind] = payload
		storedPrev := ev["previous_event_digest"]
		if storedPrev != prev {
			add(fmt.Sprintf("event_%d_previous_digest_mismatch", i))
		}
		want, err := digest(map[string]any{"sequence": i, "kind": kind, "previous_event_digest": storedPrev, "payload": payload})
		if err != nil {
			add(fmt.Sprintf("event_%d_digest_unverifiable", i))
		}
		supplied, _ := ev["event_digest"].(string)
		if supplied != want {
			add(fmt.Sprintf("event_%d_digest_mismatch", i))
		}
		prev = supplied
	}
	if n, ok := intOf(rec["replay_event_count"]); !ok || n != len(raw) {
		add("event_count_mismatch")
	}
	if rec["replay_head_digest"] != prev {
		add("head_digest_mismatch")
	}
	if p, ok := payloads["retrieval"]; ok && !reflect.DeepEqual(p, rec["retrieval"]) {
		add("retrieval_snapshot_mismatch")
	}
	if p, ok := payloads["selection"].(map[string]any); ok && !reflect.DeepEqual(p["selected_evidence"], rec["selected_evidence"]) {
		add("selection_snapshot_mismatch")
	}
	want, err := journalDigest(rec)
	if err != nil {
		add("journal_digest_unverifiable")
	}
	if s, _ := jd.(string); s != want {
		add("journal_digest_mismatch")
	}
	if s, _ := jd.(string); s == "" {
		jd = nil
	}
	return Integrity{Valid: len(errs) == 0, Errors: errs, EventCount: len(raw), HeadDigest: prev, JournalDigest: jd}
}

func path(root, runID string) string {
	return filepath.Join(root, filepath.FromSlash(Relative), runID+".json")
}

// Write creates the journal exclusively; an existing run is never overwritten.
func Write(root string, rec map[string]any) (string, error) {
	runID, _ := rec["run_id"].(string)
	if !ValidRunID(runID) {
		return "", errors.New("invalid run_id")
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}
	p := path(root, runID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".journal-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	// Link is atomic create-if-absent on every supported OS, unlike Rename.
	if err := os.Link(tmp.Name(), p); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", err
		}
		// Filesystems without hard links: exclusive create; Verify catches a torn write.
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return "", err
		}
		_, werr := f.Write(append(b, '\n'))
		if serr := f.Sync(); werr == nil {
			werr = serr
		}
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return "", werr
		}
	}
	return Relative + "/" + runID + ".json", nil
}

// Read loads a journal, refusing unsafe IDs and oversized files.
func Read(root, runID string) (map[string]any, bool) {
	if !ValidRunID(runID) {
		return nil, false
	}
	p := path(root, runID)
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxJournal {
		return nil, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var rec map[string]any
	if dec.Decode(&rec) != nil || rec == nil {
		return nil, false
	}
	return rec, true
}

// Current holds present identities to compare with a recorded run.
type Current struct {
	ConfigDigest           string
	RetrievalPolicyVersion string
	// WorkspaceFingerprint recomputes the workspace fingerprint for the recorded changed files.
	WorkspaceFingerprint  func(changed []string) string
	GraphFingerprint      string
	RepositoryFingerprint string
}

func str(m any, k string) string {
	mm, _ := m.(map[string]any)
	s, _ := mm[k].(string)
	return s
}

// Compatibility compares recorded identities with the current workspace.
func Compatibility(runID string, rec map[string]any, cur Current) map[string]any {
	changed, err := validChanged(rec)
	if err != nil {
		return map[string]any{"run_id": runID, "compatible": false, "mismatches": []string{"invalid_changed_files"}, "recorded": map[string]any{}, "current": map[string]any{}}
	}
	recorded := map[string]string{
		"config_digest":            str(rec["policy_identity"], "config_digest"),
		"retrieval_policy_version": str(rec["policy_identity"], "retrieval_policy_version"),
		"workspace_fingerprint":    str(rec["workspace_state"], "fingerprint"),
		"graph_fingerprint":        str(rec["graph_state"], "fingerprint"),
	}
	current := map[string]string{
		"config_digest":            cur.ConfigDigest,
		"retrieval_policy_version": cur.RetrievalPolicyVersion,
		"workspace_fingerprint":    cur.WorkspaceFingerprint(changed),
		"graph_fingerprint":        cur.GraphFingerprint,
	}
	if r := str(rec["repository_state"], "fingerprint"); r != "" {
		recorded["repository_fingerprint"], current["repository_fingerprint"] = r, cur.RepositoryFingerprint
	}
	mismatches := []string{}
	for k := range current {
		if current[k] != recorded[k] {
			mismatches = append(mismatches, k)
		}
	}
	slices.Sort(mismatches)
	return map[string]any{"run_id": runID, "compatible": len(mismatches) == 0, "mismatches": mismatches, "recorded": recorded, "current": current}
}

// Replay reconstructs a verified run without executing anything. Events are
// released only when integrity verification passes.
func Replay(root, runID string, cur Current) map[string]any {
	rec, ok := Read(root, runID)
	if !ok {
		return map[string]any{
			"run_id": runID, "found": false, "schema_version": nil, "replay_mode": nil,
			"read_only": true, "external_execution_performed": false,
			"integrity":       Integrity{Errors: []string{"missing_journal"}},
			"compatibility":   map[string]any{"run_id": runID, "compatible": false, "mismatches": []string{"missing_journal"}},
			"events_released": false, "events": []any{},
		}
	}
	integrity := Verify(rec, runID)
	compat := map[string]any{"run_id": runID, "compatible": false, "mismatches": []string{"invalid_journal"}, "recorded": map[string]any{}, "current": map[string]any{}}
	events := []any{}
	if integrity.Valid {
		compat = Compatibility(runID, rec, cur)
		events, _ = rec["replay_events"].([]any)
	}
	return map[string]any{
		"run_id": runID, "found": true, "schema_version": rec["schema_version"], "replay_mode": rec["replay_mode"],
		"read_only": true, "external_execution_performed": false,
		"integrity": integrity, "compatibility": compat,
		"replay_head_digest": rec["replay_head_digest"], "journal_digest": rec[digestField],
		"events_released": integrity.Valid, "events": events,
	}
}
