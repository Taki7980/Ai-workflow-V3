package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

// Freshness summarizes how an on-disk index compares to the working tree.
type Freshness struct {
	Indexed  bool `json:"indexed"`
	Stale    bool `json:"stale"`
	Added    int  `json:"added"`
	Modified int  `json:"modified"`
	Removed  int  `json:"removed"`
}

// CheckFreshness compares repo's on-disk index with its working tree without
// reading file contents: files are "modified" when their size or mtime
// differ. It is a cheap health signal; FileStale gives exact per-file answers.
func CheckFreshness(ctx context.Context, controlRoot string, repo workspace.Repository) (Freshness, error) {
	idx, err := Load(controlRoot, repo)
	if errors.Is(err, os.ErrNotExist) {
		return Freshness{Indexed: false, Stale: true}, nil
	}
	if err != nil {
		return Freshness{}, err
	}
	repoRoot := RepoRoot(controlRoot, repo)
	rels, err := listSourceFiles(ctx, repoRoot)
	if err != nil {
		return Freshness{}, err
	}
	f := Freshness{Indexed: true}
	seen := make(map[string]struct{}, len(rels))
	for _, rel := range rels {
		seen[rel] = struct{}{}
		old, ok := idx.Files[rel]
		if !ok {
			f.Added++
			continue
		}
		st, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil || st.Size() != old.Size || st.ModTime().UnixNano() != old.MTimeNS {
			f.Modified++
		}
	}
	for rel := range idx.Files {
		if _, ok := seen[rel]; !ok {
			f.Removed++
		}
	}
	f.Stale = f.Added+f.Modified+f.Removed > 0
	return f, nil
}

// FileStale reports whether the indexed state of rel no longer matches the
// file on disk. A missing file is stale. When size and mtime match but the
// entry is racily clean, the content is re-hashed to decide.
func (idx Index) FileStale(repoRoot, rel string) bool {
	old, ok := idx.Files[rel]
	if !ok {
		return true
	}
	path := filepath.Join(repoRoot, filepath.FromSlash(rel))
	st, err := os.Stat(path)
	if err != nil {
		return true
	}
	if st.Size() != old.Size || st.ModTime().UnixNano() != old.MTimeNS {
		return true
	}
	if statClean(old, st, idx.BuiltAtNS) {
		return false
	}
	digest, err := hashFile(path)
	return err != nil || digest != old.SHA256
}

// hashFile returns the hex SHA-256 of the file at path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
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
