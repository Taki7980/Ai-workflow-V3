package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/storage"
)

const RegistryVersion = 1

var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, ".ai": true, "ai-workspace": true,
	".venv": true, "venv": true, "node_modules": true, ".tox": true,
	".mypy_cache": true, ".pytest_cache": true, "__pycache__": true,
	"dist": true, "build": true,
}

type Repository struct {
	RepositoryID   string  `json:"repository_id"`
	Name           string  `json:"name"`
	RelativePath   string  `json:"relative_path"`
	GitDir         *string `json:"git_dir"`
	RemoteIdentity *string `json:"remote_identity"`
	HeadRef        *string `json:"head_ref"`
	HeadSHA        *string `json:"head_sha"`
	Included       bool    `json:"included"`
	Reason         string  `json:"reason"`
}

type Registry struct {
	Version        int          `json:"version"`
	ReviewRequired bool         `json:"review_required"`
	Repositories   []Repository `json:"repositories"`
}

// RegistryPath returns the on-disk location of the repository registry file
// for the workspace rooted at root.
func RegistryPath(root string) string {
	return filepath.Join(root, "ai-workspace", "config", "repositories.json")
}

// RepositoryID deterministically derives a repository identifier by hashing
// its relative path together with its remote identity, if any.
func RepositoryID(rel, remote string) string {
	var remoteValue any
	if remote != "" {
		remoteValue = remote
	}
	b, _ := json.Marshal(map[string]any{"relative_path": rel, "remote_identity": remoteValue})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// RemoteIdentity normalizes a raw git remote URL (SSH shorthand, standard
// URL, or otherwise) into a stable "host/path" identity, falling back to an
// opaque hash when the format cannot be parsed.
func RemoteIdentity(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "@") && strings.Contains(raw, ":") && !strings.Contains(raw, "://") {
		parts := strings.SplitN(raw, ":", 2)
		hostPart := parts[0]
		host := hostPart
		if at := strings.LastIndex(hostPart, "@"); at >= 0 {
			host = hostPart[at+1:]
		}
		path := strings.Trim(parts[1], "/")
		path = strings.TrimSuffix(path, ".git")
		if host != "" && path != "" {
			return strings.ToLower(host) + "/" + path
		}
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		host := parsed.Hostname()
		if host == "" {
			host = parsed.Host
		}
		path := strings.Trim(parsed.Path, "/")
		path = strings.TrimSuffix(path, ".git")
		if host != "" && path != "" {
			return strings.ToLower(host) + "/" + path
		}
	}
	h := sha256.Sum256([]byte(raw))
	return "opaque:" + hex.EncodeToString(h[:])[:16]
}

// Discover walks the directory tree under root up to maxDepth, skipping
// well-known non-project directories, and returns metadata for every Git
// repository found (including nested ones), sorted for deterministic output.
func Discover(ctx context.Context, root string, maxDepth int, autoInclude bool) ([]Repository, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var found []Repository
	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrPermission) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return nil
		}
		if rel != "." {
			depth := len(strings.Split(filepath.ToSlash(rel), "/"))
			if depth > maxDepth {
				return filepath.SkipDir
			}
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
		}
		ok, _ := isRepository(path)
		if !ok {
			return nil
		}
		repo := metadata(ctx, path, abs, autoInclude)
		found = append(found, repo)
		return nil // V2 discovers nested Git roots too.
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(found, func(i, j int) bool {
		li, lj := strings.ToLower(found[i].RelativePath), strings.ToLower(found[j].RelativePath)
		if li != lj {
			return li < lj
		}
		ri, rj := deref(found[i].RemoteIdentity), deref(found[j].RemoteIdentity)
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(found[i].Name) < strings.ToLower(found[j].Name)
	})
	return found, nil
}

// isRepository reports whether path contains a .git directory or gitdir
// file (as used by worktrees/submodules), returning the resolved git
// directory path when found.
func isRepository(path string) (bool, string) {
	dot := filepath.Join(path, ".git")
	st, err := os.Stat(dot)
	if err != nil {
		return false, ""
	}
	if st.IsDir() {
		return true, dot
	}
	if st.Mode().IsRegular() {
		b, err := os.ReadFile(dot)
		if err != nil {
			return false, ""
		}
		s := strings.TrimSpace(string(b))
		if strings.HasPrefix(strings.ToLower(s), "gitdir:") {
			p := strings.TrimSpace(s[len("gitdir:"):])
			if !filepath.IsAbs(p) {
				p = filepath.Join(path, p)
			}
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				return true, p
			}
		}
	}
	return false, ""
}

// metadata gathers Git metadata (remote, HEAD ref/SHA, git dir) for the
// repository at repoRoot and builds its Repository record relative to controlRoot.
func metadata(ctx context.Context, repoRoot, controlRoot string, include bool) Repository {
	rel, _ := filepath.Rel(controlRoot, repoRoot)
	rel = filepath.ToSlash(rel)
	if rel == "" {
		rel = "."
	}
	gitDirRaw, _ := gitText(ctx, repoRoot, "rev-parse", "--absolute-git-dir")
	remoteRaw, _ := gitText(ctx, repoRoot, "config", "--get", "remote.origin.url")
	headRefRaw, _ := gitText(ctx, repoRoot, "symbolic-ref", "--quiet", "HEAD")
	headSHARaw, _ := gitText(ctx, repoRoot, "rev-parse", "--verify", "HEAD")
	remote := RemoteIdentity(remoteRaw)
	gitDir := relativePtr(gitDirRaw, controlRoot)
	remotePtr := nonEmptyPtr(remote)
	headRef := nonEmptyPtr(headRefRaw)
	headSHA := nonEmptyPtr(headSHARaw)
	return Repository{
		RepositoryID: RepositoryID(rel, remote), Name: filepath.Base(repoRoot), RelativePath: rel,
		GitDir: gitDir, RemoteIdentity: remotePtr, HeadRef: headRef, HeadSHA: headSHA,
		Included: include, Reason: "discovered",
	}
}

// gitText runs a git command with the given args in root, bounded by a
// timeout, and returns its trimmed stdout output.
func gitText(parent context.Context, root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	b, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Save writes the given repositories as the workspace registry for root,
// marking it as requiring review.
func Save(root string, repos []Repository) error {
	return storage.WriteJSON(RegistryPath(root), Registry{Version: RegistryVersion, ReviewRequired: true, Repositories: repos})
}

// Load reads the workspace registry for root, validating its version,
// review flag, repository IDs, and uniqueness before returning it.
func Load(root string) (Registry, error) {
	b, err := os.ReadFile(RegistryPath(root))
	if err != nil {
		return Registry{}, err
	}
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return Registry{}, err
	}
	if r.Version != RegistryVersion || !r.ReviewRequired {
		return Registry{}, fmt.Errorf("invalid or unsupported repository registry")
	}
	seen := map[string]bool{}
	for i := range r.Repositories {
		repo := &r.Repositories[i]
		remote := deref(repo.RemoteIdentity)
		if repo.RepositoryID != RepositoryID(repo.RelativePath, remote) {
			return Registry{}, fmt.Errorf("invalid repository id for %s", repo.RelativePath)
		}
		key := repo.RelativePath + "\x00" + remote
		if seen[key] {
			return Registry{}, fmt.Errorf("duplicate repository entry %s", repo.RelativePath)
		}
		seen[key] = true
	}
	return r, nil
}

// Summary is the V2 `repos list` payload: the registry plus counts and a
// path-free identity fingerprint.
type Summary struct {
	Path           string       `json:"path"`
	Version        int          `json:"version"`
	ReviewRequired bool         `json:"review_required"`
	Discovered     int          `json:"discovered"`
	Accepted       int          `json:"accepted"`
	Fingerprint    string       `json:"fingerprint"`
	Repositories   []Repository `json:"repositories"`
}

// Summarize builds the V2 registry_summary payload for repos.
func Summarize(root string, repos []Repository) Summary {
	accepted := 0
	ids := make([]map[string]any, 0, len(repos))
	for _, r := range repos {
		if r.Included {
			accepted++
		}
		ids = append(ids, map[string]any{
			"repository_id": r.RepositoryID, "relative_path": r.RelativePath,
			"remote_identity": r.RemoteIdentity, "head_ref": r.HeadRef,
			"head_sha": r.HeadSHA, "included": r.Included,
		})
	}
	if repos == nil {
		repos = []Repository{}
	}
	return Summary{
		Path: RegistryPath(root), Version: RegistryVersion, ReviewRequired: true,
		Discovered: len(repos), Accepted: accepted, Fingerprint: digest(ids), Repositories: repos,
	}
}

// Refresh rediscovers repositories while preserving explicit include/exclude
// decisions (V2 refresh_registry). New repositories get includeNew.
func Refresh(ctx context.Context, root string, maxDepth int, includeNew bool) ([]Repository, error) {
	existing := map[string]Repository{}
	reg, err := Load(root)
	switch {
	case err == nil:
		for _, r := range reg.Repositories {
			existing[r.RelativePath+"\x00"+deref(r.RemoteIdentity)] = r
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	found, err := Discover(ctx, root, maxDepth, includeNew)
	if err != nil {
		return nil, err
	}
	for i := range found {
		r := &found[i]
		prev, ok := existing[r.RelativePath+"\x00"+deref(r.RemoteIdentity)]
		switch {
		case !ok:
			if includeNew {
				r.Reason = "auto-discovered"
			}
		case includeNew && !prev.Included && prev.Reason == "discovered":
			// Migrate registries from passive-discovery setups where every repo started disabled.
			r.Included, r.Reason = true, "auto-discovered"
		default:
			r.Included, r.Reason = prev.Included, prev.Reason
		}
	}
	return found, Save(root, found)
}

// SetIncluded flips inclusion for exactly one repository matched by relative
// path, repository ID, remote identity, or name (V2 set_repository_included).
func SetIncluded(root, selector string, included bool) ([]Repository, Repository, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, Repository{}, errors.New("repository selector must not be blank")
	}
	reg, err := Load(root)
	if err != nil {
		return nil, Repository{}, err
	}
	match := -1
	for i, r := range reg.Repositories {
		if selector == r.RelativePath || selector == r.RepositoryID || selector == deref(r.RemoteIdentity) || selector == r.Name {
			if match >= 0 {
				return nil, Repository{}, fmt.Errorf("repository selector is ambiguous: %s", selector)
			}
			match = i
		}
	}
	if match < 0 {
		return nil, Repository{}, fmt.Errorf("repository not found: %s", selector)
	}
	r := &reg.Repositories[match]
	r.Included, r.Reason = included, "manual-exclude"
	if included {
		r.Reason = "manual-include"
	}
	return reg.Repositories, *r, Save(root, reg.Repositories)
}

// relativePtr returns a pointer to path expressed relative to root, or nil
// if path is empty or escapes root.
func relativePtr(path, root string) *string {
	if path == "" {
		return nil
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return nil
	}
	s := filepath.ToSlash(rel)
	return &s
}

// nonEmptyPtr returns a pointer to s, or nil if s is empty.
func nonEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// deref returns *s, or the empty string if s is nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
