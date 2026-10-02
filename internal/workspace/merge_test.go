package workspace

import (
	"strings"
	"testing"
)

func repo(rel string, remote string, included bool, reason string) Repository {
	var remotePtr *string
	if remote != "" {
		remotePtr = &remote
	}
	return Repository{
		RepositoryID:   RepositoryID(rel, remote),
		Name:           rel,
		RelativePath:   rel,
		RemoteIdentity: remotePtr,
		Included:       included,
		Reason:         reason,
	}
}

func byPath(repos []Repository) map[string]Repository {
	out := map[string]Repository{}
	for _, r := range repos {
		out[r.RelativePath] = r
	}
	return out
}

// These cases mirror V2 ai_workflow/repository_registry.py refresh_registry
// at the pinned compatibility commit.
func TestMergePreservesDecisionsLikeV2(t *testing.T) {
	previous := []Repository{
		repo("kept-out", "github.com/a/kept-out", false, ReasonManualExclude),
		repo("kept-in", "", true, ReasonManualInclude),
		repo("passive", "", false, ReasonDiscovered),
	}
	discovered := []Repository{
		repo("kept-out", "github.com/a/kept-out", true, ReasonDiscovered),
		repo("kept-in", "", false, ReasonDiscovered),
		repo("passive", "", false, ReasonDiscovered),
		repo("brand-new", "", false, ReasonDiscovered),
	}

	t.Run("setup includes new and migrates passive", func(t *testing.T) {
		got := byPath(Merge(previous, discovered, true))
		want := map[string]struct {
			included bool
			reason   string
		}{
			"kept-out":  {false, ReasonManualExclude},
			"kept-in":   {true, ReasonManualInclude},
			"passive":   {true, ReasonAutoDiscovered},
			"brand-new": {true, ReasonAutoDiscovered},
		}
		for path, w := range want {
			if got[path].Included != w.included || got[path].Reason != w.reason {
				t.Errorf("%s: included=%v reason=%q want %v %q", path, got[path].Included, got[path].Reason, w.included, w.reason)
			}
		}
	})

	t.Run("refresh never auto-includes", func(t *testing.T) {
		got := byPath(Merge(previous, discovered, false))
		if got["brand-new"].Included || got["brand-new"].Reason != ReasonDiscovered {
			t.Errorf("brand-new=%+v", got["brand-new"])
		}
		if got["passive"].Included {
			t.Error("passive repository must stay excluded on refresh")
		}
		if got["kept-out"].Included || !got["kept-in"].Included {
			t.Error("explicit decisions not preserved")
		}
	})

	t.Run("vanished repositories are dropped", func(t *testing.T) {
		got := Merge(previous, discovered[:1], true)
		if len(got) != 1 || got[0].RelativePath != "kept-out" {
			t.Fatalf("got=%+v", got)
		}
	})
}

func TestMergeTreatsRemoteChangeAsNewRepository(t *testing.T) {
	previous := []Repository{repo("svc", "github.com/a/old", false, ReasonManualExclude)}
	discovered := []Repository{repo("svc", "github.com/a/new", false, ReasonDiscovered)}
	got := Merge(previous, discovered, true)
	if !got[0].Included || got[0].Reason != ReasonAutoDiscovered {
		t.Fatalf("got=%+v", got[0])
	}
}

func TestSetIncludedSelectors(t *testing.T) {
	root := t.TempDir()
	repos := []Repository{
		repo("services/api", "github.com/acme/api", true, ReasonAutoDiscovered),
		repo("web", "", true, ReasonAutoDiscovered),
	}
	if err := Save(root, repos); err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"services/api", "github.com/acme/api", repos[0].RepositoryID} {
		if _, err := SetIncluded(root, selector, false); err != nil {
			t.Fatalf("selector %q: %v", selector, err)
		}
		reg, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		if got := byPath(reg.Repositories)["services/api"]; got.Included || got.Reason != ReasonManualExclude {
			t.Fatalf("selector %q: %+v", selector, got)
		}
		if _, err := SetIncluded(root, selector, true); err != nil {
			t.Fatal(err)
		}
	}
	reg, _ := Load(root)
	if got := byPath(reg.Repositories)["services/api"]; !got.Included || got.Reason != ReasonManualInclude {
		t.Fatalf("include: %+v", got)
	}
}

func TestSetIncludedRejectsUnknownBlankAndAmbiguous(t *testing.T) {
	root := t.TempDir()
	a := repo("a/shared", "", true, ReasonAutoDiscovered)
	b := repo("b/shared", "", true, ReasonAutoDiscovered)
	a.Name, b.Name = "shared", "shared"
	if err := Save(root, []Repository{a, b}); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{"": "blank", "  ": "blank", "nope": "not found", "shared": "ambiguous"}
	for selector, want := range cases {
		_, err := SetIncluded(root, selector, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("selector %q: err=%v want %q", selector, err, want)
		}
	}
}

func TestLoadOrEmpty(t *testing.T) {
	root := t.TempDir()
	reg, err := LoadOrEmpty(root)
	if err != nil || len(reg.Repositories) != 0 || reg.Version != RegistryVersion {
		t.Fatalf("reg=%+v err=%v", reg, err)
	}
}
