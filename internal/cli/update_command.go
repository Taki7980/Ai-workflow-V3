package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/version"
)

// Release endpoints; tests point them at a local server.
var (
	latestReleaseURL = "https://api.github.com/repos/Taki7980/Ai-workflow-V3/releases/latest"
	releaseBaseURL   = "https://github.com/Taki7980/Ai-workflow-V3/releases/download"
)

var releaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// updateCmd replaces the running binary with the latest GitHub release.
func updateCmd(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(errOut)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fmt.Fprintln(errOut, "update:", err)
		return 1
	}
	latest, err := selfUpdate(exe, version.Version)
	if err != nil {
		fmt.Fprintln(errOut, "update:", err)
		return 1
	}
	if latest == version.Version {
		fmt.Fprintf(out, "ai-workflow %s is the latest release\n", latest)
		return 0
	}
	fmt.Fprintf(out, "Updated ai-workflow %s to %s at %s (SHA-256 verified)\n", version.Version, latest, exe)
	return 0
}

// selfUpdate installs the latest release over exe unless current already is
// that release, verifying the download against the release SHA256SUMS like
// the installers do. It returns the latest release version.
func selfUpdate(exe, current string) (string, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	body, err := download(client, latestReleaseURL, 1<<20)
	if err != nil {
		return "", err
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", fmt.Errorf("parse latest release: %w", err)
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	if !releaseVersion.MatchString(latest) {
		return "", fmt.Errorf("unexpected latest release tag %q", rel.TagName)
	}
	if latest == current {
		return latest, nil
	}
	asset := fmt.Sprintf("ai-workflow-v%s-%s-%s", latest, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}
	base := releaseBaseURL + "/v" + latest
	sums, err := download(client, base+"/SHA256SUMS", 1<<20)
	if err != nil {
		return "", err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = strings.ToLower(f[0])
		}
	}
	if want == "" {
		return "", fmt.Errorf("no SHA-256 checksum listed for %s", asset)
	}
	bin, err := download(client, base+"/"+asset, 512<<20)
	if err != nil {
		return "", err
	}
	if sum := sha256.Sum256(bin); hex.EncodeToString(sum[:]) != want {
		return "", fmt.Errorf("SHA-256 verification failed for %s", asset)
	}
	return latest, replaceExecutable(exe, bin)
}

// replaceExecutable swaps exe for bin. A running Windows exe cannot be
// overwritten but can be renamed, so exe moves aside to exe.old first; the
// next update removes it.
func replaceExecutable(exe string, bin []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".ai-workflow-update-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(bin)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o755)
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return werr
	}
	old := exe + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.Remove(tmp.Name())
		return fmt.Errorf("close running ai-workflow processes and retry (%s is in use): %w", old, err)
	}
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		os.Rename(old, exe)
		os.Remove(tmp.Name())
		return err
	}
	os.Remove(old) // fails while this process runs from it on Windows; harmless
	return nil
}

func download(client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ai-workflow/"+version.Version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("GET %s: response larger than %d bytes", url, limit)
	}
	return b, nil
}
