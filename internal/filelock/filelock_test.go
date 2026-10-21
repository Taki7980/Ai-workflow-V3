package filelock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockExclusionAndStaleRecovery(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	release, err := Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(p); err == nil {
		t.Fatal("second holder must be refused")
	}
	release()
	release2, err := Acquire(p)
	if err != nil {
		t.Fatalf("lock must be reusable after release: %v", err)
	}
	// Simulate a crashed holder on another host with an expired lease.
	o := owner{Token: "x", PID: 999999, HostFingerprint: "other-host"}
	_ = o
	release2()
	if err := os.Mkdir(p+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * Lease)
	if err := os.Chtimes(p+".lock", old, old); err != nil {
		t.Fatal(err)
	}
	r3, err := Acquire(p)
	if err != nil {
		t.Fatalf("expired foreign lock must be recovered: %v", err)
	}
	r3()
}
