package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Registry reasons. These strings are part of the V2 registry contract.
const (
	ReasonDiscovered     = "discovered"
	ReasonAutoDiscovered = "auto-discovered"
	ReasonManualInclude  = "manual-include"
	ReasonManualExclude  = "manual-exclude"
)

// HeadSHA returns the commit SHA that HEAD resolves to for the repository at
// repoRoot, or "" when it cannot be determined (no git, unborn branch, ...).
func HeadSHA(ctx context.Context, repoRoot string) string {
	sha, err := gitText(ctx, repoRoot, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return ""
	}
	return sha
}

// Merge combines freshly discovered repositories with a previous registry,
// mirroring V2's refresh_registry semantics:
//
//   - a repository not seen before is included only when includeNew is set,
//     with reason "auto-discovered" (otherwise it keeps its discovery reason);
//   - a previously known repository keeps its explicit include/exclude
//     decision and reason;
//   - as a migration path, a previously known repository that is excluded with
//     the passive reason "discovered" is included when includeNew is set.
//
// Repositories are matched on (relative_path, remote_identity), the same key
// that RepositoryID hashes. Git metadata always comes from discovery.
func Merge(previous, discovered []Repository, includeNew bool) []Repository {
	existing := make(map[string]Repository, len(previous))
	for _, repo := range previous {
		existing[mergeKey(repo)] = repo
	}
	out := make([]Repository, 0, len(discovered))
	for _, repo := range discovered {
		prev, known := existing[mergeKey(repo)]
		switch {
		case !known:
			repo.Included = includeNew
			if includeNew {
				repo.Reason = ReasonAutoDiscovered
			} else if repo.Reason == "" {
				repo.Reason = ReasonDiscovered
			}
		case includeNew && !prev.Included && prev.Reason == ReasonDiscovered:
			repo.Included = true
			repo.Reason = ReasonAutoDiscovered
		default:
			repo.Included = prev.Included
			repo.Reason = prev.Reason
		}
		out = append(out, repo)
	}
	return out
}

// LoadOrEmpty loads the registry for root, returning an empty registry when
// none exists yet. An existing but invalid registry is an error, so a corrupt
// or foreign file is never silently overwritten.
func LoadOrEmpty(root string) (Registry, error) {
	reg, err := Load(root)
	if errors.Is(err, os.ErrNotExist) {
		return Registry{Version: RegistryVersion, ReviewRequired: true, Repositories: []Repository{}}, nil
	}
	return reg, err
}

// SetIncluded includes or excludes exactly one repository, chosen by its
// relative path, repository ID, remote identity, or name, and persists the
// registry. It mirrors V2's set_repository_included, including rejecting
// unknown and ambiguous selectors.
func SetIncluded(root, selector string, included bool) (Repository, error) {
	value := strings.TrimSpace(selector)
	if value == "" {
		return Repository{}, errors.New("repository selector must not be blank")
	}
	reg, err := Load(root)
	if err != nil {
		return Repository{}, err
	}
	match := -1
	for i, repo := range reg.Repositories {
		if !matchesSelector(repo, value) {
			continue
		}
		if match >= 0 {
			return Repository{}, fmt.Errorf("repository selector is ambiguous: %s", value)
		}
		match = i
	}
	if match < 0 {
		return Repository{}, fmt.Errorf("repository not found: %s", value)
	}
	target := &reg.Repositories[match]
	target.Included = included
	if included {
		target.Reason = ReasonManualInclude
	} else {
		target.Reason = ReasonManualExclude
	}
	if err := Save(root, reg.Repositories); err != nil {
		return Repository{}, err
	}
	return *target, nil
}

// matchesSelector reports whether selector names repo by any stable handle.
func matchesSelector(repo Repository, selector string) bool {
	return selector == repo.RelativePath ||
		selector == repo.RepositoryID ||
		(repo.RemoteIdentity != nil && selector == *repo.RemoteIdentity) ||
		selector == repo.Name
}

// mergeKey is the identity used to match repositories across refreshes.
func mergeKey(repo Repository) string {
	return repo.RelativePath + "\x00" + deref(repo.RemoteIdentity)
}
