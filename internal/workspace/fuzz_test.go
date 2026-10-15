package workspace

import (
	"strings"
	"testing"
)

// FuzzRemoteIdentity checks remote normalization never panics and never
// echoes URL credentials into the persisted identity.
func FuzzRemoteIdentity(f *testing.F) {
	for _, s := range []string{"git@github.com:org/repo.git", "https://user:pw@host/x.git", "ssh://h/p", "::::", "@:"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		id := RemoteIdentity(raw)
		if at := strings.Index(raw, "://"); at >= 0 {
			rest := raw[at+3:]
			if cred, _, ok := strings.Cut(rest, "@"); ok && strings.Contains(cred, ":") && !strings.ContainsAny(cred, "/?#") {
				if _, pw, _ := strings.Cut(cred, ":"); len(pw) >= 4 && strings.Contains(id, pw) && !strings.Contains(rest[len(cred)+1:], pw) {
					t.Fatalf("credential %q leaked into identity %q", pw, id)
				}
			}
		}
	})
}
