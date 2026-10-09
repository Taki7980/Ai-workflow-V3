package structural

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/storage"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

const manifestSchema = 1

// Status reports whether central structural state matches the repository.
type Status struct {
	Ready   bool   `json:"ready"`
	Reason  string `json:"reason"`
	DataDir string `json:"data_dir"`
}

type GraphManifest struct {
	ManifestSchema         int     `json:"manifest_schema"`
	RepositoryRelativePath string  `json:"repository_relative_path"`
	RepositoryFingerprint  string  `json:"repository_fingerprint"`
	GitHead                *string `json:"git_head"`
	GraphSHA256            string  `json:"graph_sha256"`
	CRGVersion             string  `json:"crg_version"`
	GenerationMode         string  `json:"generation_mode"`
	GeneratedAt            string  `json:"generated_at"`
}

type ScipManifest struct {
	ManifestSchema         int     `json:"manifest_schema"`
	RepositoryRelativePath string  `json:"repository_relative_path"`
	RepositoryFingerprint  string  `json:"repository_fingerprint"`
	GitHead                *string `json:"git_head"`
	Language               string  `json:"language"`
	Indexer                string  `json:"indexer"`
	IndexSHA256            string  `json:"index_sha256"`
	JSONSHA256             string  `json:"json_sha256"`
	GeneratedAt            string  `json:"generated_at"`
}

func sha256File(p string) (string, error) {
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

func gitHead(ctx context.Context, dir string) *string {
	cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	h := strings.TrimSpace(string(out))
	return &h
}

// RepoFingerprint hashes git HEAD plus the content state of every changed
// path. Control state under ai-workspace/ is excluded so writing manifests,
// briefs or handoffs never makes the repository look changed.
func RepoFingerprint(ctx context.Context, root string) (fingerprint string, head *string) {
	head = gitHead(ctx, root)
	changed := []string{}
	for _, p := range workspace.GitStatus(ctx, root) {
		if !strings.HasPrefix(p, "ai-workspace/") {
			changed = append(changed, p)
		}
	}
	b, _ := workspace.CanonicalJSON(map[string]any{
		"schema":        1,
		"git_head":      head,
		"changed_files": workspace.ChangedState(root, changed),
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), head
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

func writeManifest(dir string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return storage.WriteFileAtomic(filepath.Join(dir, "manifest.json"), append(b, '\n'))
}

// WriteGraphManifest records provenance for an existing graph.db.
func WriteGraphManifest(ctx context.Context, ws, rel, crgVersion, mode string) (GraphManifest, error) {
	dir, err := GraphDir(ws, rel)
	if err != nil {
		return GraphManifest{}, err
	}
	root, err := repoRoot(ws, rel)
	if err != nil {
		return GraphManifest{}, err
	}
	sum, err := sha256File(filepath.Join(dir, "graph.db"))
	if err != nil {
		return GraphManifest{}, err
	}
	fp, head := RepoFingerprint(ctx, root)
	m := GraphManifest{manifestSchema, normRel(rel), fp, head, sum, crgVersion, mode, now()}
	return m, writeManifest(dir, m)
}

// WriteScipManifest records provenance for index.scip and index.json.
func WriteScipManifest(ctx context.Context, ws, rel, language, indexer string) (ScipManifest, error) {
	dir, err := ScipDir(ws, rel)
	if err != nil {
		return ScipManifest{}, err
	}
	root, err := repoRoot(ws, rel)
	if err != nil {
		return ScipManifest{}, err
	}
	idx, err := sha256File(filepath.Join(dir, "index.scip"))
	if err != nil {
		return ScipManifest{}, err
	}
	js, err := sha256File(filepath.Join(dir, "index.json"))
	if err != nil {
		return ScipManifest{}, err
	}
	fp, head := RepoFingerprint(ctx, root)
	m := ScipManifest{manifestSchema, normRel(rel), fp, head, language, indexer, idx, js, now()}
	return m, writeManifest(dir, m)
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// readManifest loads manifest.json and checks schema and path identity.
func readManifest(dir, rel string, into any) string {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return "manifest.json is unreadable"
	}
	var head struct {
		Schema *int   `json:"manifest_schema"`
		Rel    string `json:"repository_relative_path"`
	}
	if json.Unmarshal(b, &head) != nil || json.Unmarshal(b, into) != nil {
		return "manifest.json is unreadable"
	}
	if head.Schema == nil || *head.Schema != manifestSchema {
		return "unsupported manifest schema"
	}
	if head.Rel != normRel(rel) {
		return "repository path identity mismatch"
	}
	return ""
}

func provenance(ctx context.Context, ws, rel, fp string, head *string) string {
	root, err := repoRoot(ws, rel)
	if err != nil {
		return "unsafe repository path"
	}
	cur, curHead := RepoFingerprint(ctx, root)
	if fp != cur {
		return "repository fingerprint mismatch"
	}
	if (head == nil) != (curHead == nil) || (head != nil && *head != *curHead) {
		return "Git HEAD mismatch"
	}
	return ""
}

// GraphStatus checks graph.db bytes and provenance against the repository.
func GraphStatus(ctx context.Context, ws, rel string) Status {
	dir, err := GraphDir(ws, rel)
	if err != nil {
		return Status{Reason: "unsafe graph state path: " + err.Error()}
	}
	st := Status{DataDir: dir}
	fail := func(r string) Status { st.Reason = r; return st }
	db := filepath.Join(dir, "graph.db")
	if !isFile(db) {
		return fail("graph.db is missing")
	}
	if !isFile(filepath.Join(dir, "manifest.json")) {
		return fail("manifest.json is missing")
	}
	var m GraphManifest
	if r := readManifest(dir, rel, &m); r != "" {
		return fail(r)
	}
	if sum, err := sha256File(db); err != nil {
		return fail("graph.db is unreadable")
	} else if sum != m.GraphSHA256 {
		return fail("graph hash mismatch")
	}
	if r := provenance(ctx, ws, rel, m.RepositoryFingerprint, m.GitHead); r != "" {
		return fail(r)
	}
	st.Ready, st.Reason = true, "graph provenance matches repository state"
	return st
}

// ScipStatus checks index.scip/index.json bytes and provenance.
func ScipStatus(ctx context.Context, ws, rel string) Status {
	dir, err := ScipDir(ws, rel)
	if err != nil {
		return Status{Reason: "unsafe SCIP state path: " + err.Error()}
	}
	st := Status{DataDir: dir}
	fail := func(r string) Status { st.Reason = r; return st }
	idx, js := filepath.Join(dir, "index.scip"), filepath.Join(dir, "index.json")
	if !isFile(idx) {
		return fail("index.scip is missing")
	}
	if !isFile(filepath.Join(dir, "manifest.json")) {
		return fail("manifest.json is missing")
	}
	if !isFile(js) {
		return fail("index.json is missing")
	}
	var m ScipManifest
	if r := readManifest(dir, rel, &m); r != "" {
		return fail(r)
	}
	if sum, err := sha256File(idx); err != nil || sum != m.IndexSHA256 {
		return fail("SCIP index hash mismatch")
	}
	if sum, err := sha256File(js); err != nil || sum != m.JSONSHA256 {
		return fail("SCIP JSON hash mismatch")
	}
	if r := provenance(ctx, ws, rel, m.RepositoryFingerprint, m.GitHead); r != "" {
		return fail(r)
	}
	st.Ready, st.Reason = true, "SCIP provenance matches repository state"
	return st
}
