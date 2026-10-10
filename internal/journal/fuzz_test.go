package journal

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzVerify feeds hostile journal documents to Verify: it must never panic
// and must never report an arbitrary document as valid.
func FuzzVerify(f *testing.F) {
	f.Add([]byte(`{"schema_version":"ai-workflow-run-journal-v2","replay_events":[{"sequence":0}]}`))
	f.Add([]byte(`{"run_id":"a","changed_files":[1],"replay_events":"x"}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		var rec map[string]any
		if dec.Decode(&rec) != nil || rec == nil {
			return
		}
		if Verify(rec, "").Valid {
			// Only a self-consistent journal may verify; re-check its digest.
			want, err := journalDigest(rec)
			if err != nil || rec[digestField] != want {
				t.Fatalf("inconsistent journal verified: %s", b)
			}
		}
	})
}
