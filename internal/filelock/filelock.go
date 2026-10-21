// Package filelock is V2's crash-recoverable local critical section: a lock
// directory with owner metadata, a random token and bounded stale recovery
// that never steals a lock from a live owner on the same host. It is a local
// filesystem lock, not a distributed one.
package filelock

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Lease is how long a lock may be held before it can be considered stale.
var Lease = 300 * time.Second

type owner struct {
	Token           string  `json:"token"`
	PID             int     `json:"pid"`
	HostFingerprint string  `json:"host_fingerprint"`
	AcquiredAt      string  `json:"acquired_at"`
	LeaseSeconds    float64 `json:"lease_seconds"`
}

func hostFingerprint() string {
	h, _ := os.Hostname()
	s := sha256.Sum256([]byte(h))
	return hex.EncodeToString(s[:])[:16]
}

func token() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func readOwner(lock string) owner {
	var o owner
	if b, err := os.ReadFile(filepath.Join(lock, "owner.json")); err == nil {
		_ = json.Unmarshal(b, &o)
	}
	return o
}

func recoverStale(lock string) bool {
	info, err := os.Stat(lock)
	if err != nil || time.Since(info.ModTime()) <= Lease {
		return false
	}
	o := readOwner(lock)
	if o.HostFingerprint == hostFingerprint() && o.PID > 0 && pidAlive(o.PID) {
		return false
	}
	stale := fmt.Sprintf("%s.stale-%s", lock, token()[:12])
	if os.Rename(lock, stale) != nil {
		return false
	}
	_ = os.RemoveAll(stale)
	return true
}

// Acquire takes the lock for path (path + ".lock"); call the returned release.
func Acquire(path string) (func(), error) {
	lock := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		err := os.Mkdir(lock, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if attempt == 0 && recoverStale(lock) {
			continue
		}
		return nil, fmt.Errorf("state is locked: %s", lock)
	}
	tok := token()
	b, _ := json.Marshal(owner{tok, os.Getpid(), hostFingerprint(), time.Now().UTC().Format(time.RFC3339Nano), Lease.Seconds()})
	if err := os.WriteFile(filepath.Join(lock, "owner.json"), b, 0o600); err != nil {
		_ = os.RemoveAll(lock)
		return nil, err
	}
	return func() {
		if readOwner(lock).Token == tok {
			_ = os.RemoveAll(lock)
		}
	}, nil
}
