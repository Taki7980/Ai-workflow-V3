package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ChangedFile is one changed-file identity inside a workspace fingerprint.
type ChangedFile struct {
	Path   string  `json:"path"`
	State  string  `json:"state"`
	SHA256 *string `json:"sha256"`
}

// State is the V2-shaped workspace fingerprint contract (schema 2).
type State struct {
	Root             string        `json:"root"`
	Schema           int           `json:"schema"`
	GitHead          *string       `json:"git_head"`
	IndexStateSHA256 *string       `json:"index_state_sha256"`
	ChangedFiles     []ChangedFile `json:"changed_files"`
	Fingerprint      string        `json:"fingerprint"`
}

// CanonicalJSON encodes v like Python json.dumps(sort_keys=True,
// separators=(",", ":"), ensure_ascii=False).
func CanonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func digest(v any) string {
	b, err := CanonicalJSON(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Fingerprint binds a retrieval packet to the workspace state: Git HEAD,
// index-state digest (indexFiles nil means no index), and changed-file hashes.
func Fingerprint(ctx context.Context, root string, indexFiles map[string]any, changed []string) State {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	st := State{Root: abs, Schema: 2, ChangedFiles: ChangedState(abs, changed)}
	if head, err := gitText(ctx, abs, "rev-parse", "HEAD"); err == nil && head != "" {
		st.GitHead = &head
	}
	if indexFiles != nil {
		d := digest(indexFiles)
		st.IndexStateSHA256 = &d
	}
	st.Fingerprint = digest(map[string]any{
		"schema":             st.Schema,
		"git_head":           st.GitHead,
		"index_state_sha256": st.IndexStateSHA256,
		"changed_files":      st.ChangedFiles,
	})
	return st
}

// ChangedState hashes each changed path under root (present/missing/rejected/unreadable).
func ChangedState(root string, changed []string) []ChangedFile {
	uniq := map[string]bool{}
	for _, c := range changed {
		uniq[c] = true
	}
	paths := make([]string, 0, len(uniq))
	for p := range uniq {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]ChangedFile, 0, len(paths))
	for _, rel := range paths {
		_, p, err := Within(root, rel)
		if err != nil || filepath.IsAbs(rel) {
			out = append(out, ChangedFile{Path: rel, State: "rejected"})
			continue
		}
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			out = append(out, ChangedFile{Path: rel, State: "missing"})
			continue
		}
		h, err := streamSHA256(p)
		if err != nil {
			out = append(out, ChangedFile{Path: rel, State: "unreadable"})
			continue
		}
		out = append(out, ChangedFile{Path: rel, State: "present", SHA256: &h})
	}
	return out
}

// ChangedFiles lists dirty paths in the control root and every included
// nested repository, prefixed with the repository's relative path.
func ChangedFiles(ctx context.Context, root string, reg Registry) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range GitStatus(ctx, root) {
		add(p)
	}
	for _, repo := range reg.Repositories {
		if !repo.Included || repo.RelativePath == "." || repo.RelativePath == "" {
			continue
		}
		prefix := strings.TrimSuffix(filepath.ToSlash(repo.RelativePath), "/") + "/"
		for _, p := range GitStatus(ctx, filepath.Join(root, filepath.FromSlash(repo.RelativePath))) {
			add(prefix + p)
		}
	}
	return out
}

// GitStatus returns changed file paths from `git status --porcelain=v1 -z`,
// skipping directory entries (nested repositories).
func GitStatus(ctx context.Context, dir string) []string {
	out, _ := GitStatusErr(ctx, dir)
	return out
}

// GitStatusErr is GitStatus that reports a failed or timed-out git run, so
// callers relying on "no changes" can fail closed.
func GitStatusErr(ctx context.Context, dir string) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	cmd.Dir = dir
	raw, err := cmd.Output() // untrimmed: the leading status column is significant
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	fields := strings.Split(string(raw), "\x00")
	out := []string{}
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		if f[0] == 'R' || f[0] == 'C' {
			i++ // the following field is the rename/copy source
		}
		if p := f[3:]; !strings.HasSuffix(p, "/") {
			out = append(out, p)
		}
	}
	return out, nil
}

// Within resolves raw (relative or absolute) against root, following symlinks,
// and rejects any path that escapes the root. rel is slash-separated.
func Within(root, raw string) (rel, abs string, err error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	if resolved, e := filepath.EvalSymlinks(rootAbs); e == nil {
		rootAbs = resolved
	}
	abs = raw
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(rootAbs, filepath.FromSlash(raw))
	}
	if resolved, e := filepath.EvalSymlinks(abs); e == nil {
		abs = resolved
	}
	rel, err = filepath.Rel(rootAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", errors.New("outside workspace")
	}
	return filepath.ToSlash(rel), abs, nil
}

// streamSHA256 hashes a file without loading it into memory.
func streamSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
