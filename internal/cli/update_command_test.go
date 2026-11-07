package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSelfUpdate(t *testing.T) {
	asset := "ai-workflow-v9.9.9-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}
	newBin := []byte("new binary")
	sum := sha256.Sum256(newBin)
	sums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			w.Write([]byte(`{"tag_name":"v9.9.9"}`))
		case "/download/v9.9.9/SHA256SUMS":
			w.Write([]byte(sums))
		case "/download/v9.9.9/" + asset:
			downloads++
			w.Write(newBin)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldLatest, oldBase := latestReleaseURL, releaseBaseURL
	latestReleaseURL, releaseBaseURL = srv.URL+"/latest", srv.URL+"/download"
	defer func() { latestReleaseURL, releaseBaseURL = oldLatest, oldBase }()

	exe := filepath.Join(t.TempDir(), "ai-workflow.exe")
	writeExe := func() {
		if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	readExe := func() string {
		b, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	writeExe()
	if v, err := selfUpdate(exe, "9.9.9"); err != nil || v != "9.9.9" || downloads != 0 || readExe() != "old binary" {
		t.Fatalf("current release must not download: v=%q err=%v downloads=%d", v, err, downloads)
	}

	if v, err := selfUpdate(exe, "1.0.0"); err != nil || v != "9.9.9" || readExe() != "new binary" {
		t.Fatalf("update: v=%q err=%v content=%q", v, err, readExe())
	}
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatalf("old binary left behind: %v", err)
	}

	writeExe()
	sums = strings.Repeat("0", 64) + "  " + asset + "\n"
	if _, err := selfUpdate(exe, "1.0.0"); err == nil || !strings.Contains(err.Error(), "SHA-256 verification failed") || readExe() != "old binary" {
		t.Fatalf("checksum mismatch must keep the old binary: err=%v content=%q", err, readExe())
	}
}
