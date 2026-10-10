package provider

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	RegistryEnv          = "AI_WORKFLOW_PROVIDER_REGISTRY"
	UnsafeRepoCommandEnv = "AI_WORKFLOW_ALLOW_REPO_PROVIDER_COMMANDS"
)

// UnsafeRepoCommandsEnabled reports the explicit operator opt-in for
// repository-defined provider commands.
func UnsafeRepoCommandsEnabled() bool { return truthyEnv(UnsafeRepoCommandEnv) }

func truthyEnv(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// RegistryPath is the user/admin-owned provider registry, never inside a repo.
func RegistryPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv(RegistryEnv)); p != "" {
		if !filepath.IsAbs(p) {
			return "", fmt.Errorf("%s must be an absolute path", RegistryEnv)
		}
		return filepath.Clean(p), nil
	}
	dir, err := os.UserConfigDir()
	if runtime.GOOS == "windows" {
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			dir, err = v, nil
		}
	}
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ai-workflow", "providers.json"), nil
}

// rawSpec is the JSON shape of a provider in the registry or project config.
type rawSpec struct {
	Name             string          `json:"name"`
	ProviderID       string          `json:"provider_id"`
	Command          json.RawMessage `json:"command"`
	TimeoutSeconds   *float64        `json:"timeout_seconds"`
	MaxOutputBytes   *int64          `json:"max_output_bytes"`
	MaxStderrBytes   *int64          `json:"max_stderr_bytes"`
	Intents          json.RawMessage `json:"intents"`
	EnvAllowlist     []string        `json:"env_allowlist"`
	SHA256           string          `json:"sha256"`
	ExecutableSHA256 string          `json:"executable_sha256"`
	NeutralCWD       *bool           `json:"neutral_cwd"`
	Version          string          `json:"version"`
	RuntimeProfile   string          `json:"runtime_profile"`
	Sandbox          *SandboxPolicy  `json:"sandbox"`
	Semantics        json.RawMessage `json:"semantics"`
}

func stringsOrOne(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, errors.New("expected a string or an array of strings")
	}
	return many, nil
}

func (r rawSpec) spec(defaultName string) (Spec, error) {
	argv, err := commandArgv(r.Command)
	if err != nil {
		return Spec{}, err
	}
	intents, err := stringsOrOne(r.Intents)
	if err != nil {
		return Spec{}, fmt.Errorf("provider intents: %w", err)
	}
	s := Spec{Name: r.Name, Command: argv, Intents: intents, EnvAllowlist: r.EnvAllowlist, Version: r.Version, RuntimeProfile: r.RuntimeProfile,
		ExecutableSHA256: r.ExecutableSHA256}
	if s.Name == "" {
		s.Name = defaultName
	}
	if s.ExecutableSHA256 == "" {
		s.ExecutableSHA256 = r.SHA256
	}
	if r.TimeoutSeconds != nil {
		if *r.TimeoutSeconds <= 0 || *r.TimeoutSeconds > 3600 {
			return Spec{}, errors.New("provider timeout_seconds must be > 0 and <= 3600")
		}
		s.Timeout = time.Duration(*r.TimeoutSeconds * float64(time.Second))
	}
	if r.MaxOutputBytes != nil {
		s.MaxOutputBytes = *r.MaxOutputBytes
	}
	if r.MaxStderrBytes != nil {
		s.MaxStderrBytes = *r.MaxStderrBytes
	}
	if r.NeutralCWD != nil {
		s.NeutralCWD = *r.NeutralCWD
	}
	if r.Sandbox != nil {
		s.Sandbox = *r.Sandbox
	}
	return s, s.Validate()
}

// commandArgv accepts an argv array, or a string split on whitespace without
// any shell interpretation (quotes are not supported; use an array).
func commandArgv(raw json.RawMessage) ([]string, error) {
	var argv []string
	if json.Unmarshal(raw, &argv) == nil {
		return argv, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, errors.New("provider command must be a string or argv list")
	}
	if strings.ContainsAny(s, `"'`) {
		return nil, errors.New("provider command strings may not contain quotes; use an argv array")
	}
	return strings.Fields(s), nil
}

// readRegistry reads the trust anchor after checking ownership/permissions
// on the opened file (no validate-then-swap race on POSIX).
func readRegistry(root string) (map[string]json.RawMessage, error) {
	path, err := RegistryPath()
	if err != nil {
		return nil, err
	}
	if insideRoot(root, path) {
		return nil, errors.New("trusted provider registry must live outside the repository")
	}
	b, err := readTrustedFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Providers map[string]json.RawMessage `json:"providers"`
	}
	if json.Unmarshal(b, &doc) != nil || doc.Providers == nil {
		return nil, errors.New("trusted provider registry must contain a providers object")
	}
	return doc.Providers, nil
}

// insideRoot is a lexical-plus-symlink containment check for paths that may not exist.
func insideRoot(root, p string) bool {
	r, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	if ev, err := filepath.EvalSymlinks(r); err == nil {
		r = ev
	}
	c := p
	if ev, err := filepath.EvalSymlinks(p); err == nil {
		c = ev
	}
	rel, err := filepath.Rel(r, c)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// validatedExecutable checks a registry command: absolute, not a symlink,
// regular file outside the repository, pinned digest matching, and no
// repository-owned path arguments (a pinned interpreter may not run repo scripts).
func validatedExecutable(root string, argv []string, digest string) ([]string, string, error) {
	if len(argv) == 0 {
		return nil, "", errors.New("trusted provider command must be a non-empty argv array")
	}
	exe := argv[0]
	if !filepath.IsAbs(exe) {
		return nil, "", errors.New("trusted provider executable must be an absolute path")
	}
	fi, err := os.Lstat(exe)
	if err != nil {
		return nil, "", errors.New("trusted provider executable does not exist")
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, "", errors.New("trusted provider executable path may not be a symlink")
	}
	if !fi.Mode().IsRegular() {
		return nil, "", errors.New("trusted provider executable is not a regular file")
	}
	if insideRoot(root, exe) {
		return nil, "", errors.New("trusted provider executable must live outside the repository")
	}
	want, err := normalizeDigest(digest)
	if err != nil {
		return nil, "", errors.New("trusted provider requires a sha256 digest")
	}
	got, err := sha256File(exe)
	if err != nil || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return nil, "", errors.New("trusted provider executable digest mismatch")
	}
	for _, arg := range argv[1:] {
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		cand := arg
		if !filepath.IsAbs(cand) {
			cand = filepath.Join(root, cand)
		}
		if _, err := os.Stat(cand); err == nil && insideRoot(root, cand) {
			return nil, "", errors.New("trusted provider command may not execute repository-owned path arguments")
		}
	}
	return argv, want, nil
}

// ResolveTrusted builds a spec whose executable authority comes only from the
// registry. The project may select the ID and tighten timeout/output/intents.
func ResolveTrusted(root, providerID string, project rawSpec, defaultName string) (Spec, error) {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return Spec{}, errors.New("provider_id must not be blank")
	}
	providers, err := readRegistry(root)
	if err != nil {
		return Spec{}, err
	}
	raw, ok := providers[providerID]
	if !ok {
		return Spec{}, fmt.Errorf("trusted provider is not registered: %s", providerID)
	}
	var trusted rawSpec
	if err := json.Unmarshal(raw, &trusted); err != nil {
		return Spec{}, fmt.Errorf("trusted provider %s is malformed", providerID)
	}
	argv, err := commandArgv(trusted.Command)
	if err != nil {
		return Spec{}, err
	}
	digest := trusted.SHA256
	if digest == "" {
		digest = trusted.ExecutableSHA256
	}
	argv, digest, err = validatedExecutable(root, argv, digest)
	if err != nil {
		return Spec{}, err
	}
	if trusted.NeutralCWD == nil {
		t := true
		trusted.NeutralCWD = &t
	}
	if trusted.RuntimeProfile == "" {
		trusted.RuntimeProfile = "restricted"
	}
	b, _ := json.Marshal(argv)
	trusted.Command, trusted.SHA256, trusted.ExecutableSHA256 = b, digest, ""
	s, err := trusted.spec(providerID)
	if err != nil {
		return Spec{}, err
	}
	s.ExecutableTrust = TrustRegistry
	if project.Name != "" {
		s.Name = project.Name
	} else if defaultName != "" {
		s.Name = defaultName
	}
	if project.TimeoutSeconds != nil && *project.TimeoutSeconds > 0 {
		s.Timeout = min(s.Timeout, time.Duration(*project.TimeoutSeconds*float64(time.Second)))
	}
	if project.MaxOutputBytes != nil && *project.MaxOutputBytes > 0 {
		s.MaxOutputBytes = min(s.MaxOutputBytes, *project.MaxOutputBytes)
	}
	if intents, err := stringsOrOne(project.Intents); err != nil {
		return Spec{}, err
	} else if len(intents) > 0 {
		s.Intents = intents
	}
	return s, nil
}

// ResolveProject resolves a provider declared in repository config. A
// provider_id defers to the trusted registry; inline commands are refused
// unless the operator opts in with AI_WORKFLOW_ALLOW_REPO_PROVIDER_COMMANDS.
func ResolveProject(root string, raw json.RawMessage, defaultName string) (Spec, error) {
	var r rawSpec
	if err := json.Unmarshal(raw, &r); err != nil {
		return Spec{}, errors.New("provider configuration must be an object")
	}
	if strings.TrimSpace(r.ProviderID) != "" {
		forbidden := []string{}
		check := func(set bool, name string) {
			if set {
				forbidden = append(forbidden, name)
			}
		}
		check(len(r.Command) > 0 && string(r.Command) != `""` && string(r.Command) != "[]" && string(r.Command) != "null", "command")
		check(len(r.EnvAllowlist) > 0, "env_allowlist")
		check(r.SHA256 != "", "sha256")
		check(r.ExecutableSHA256 != "", "executable_sha256")
		check(r.NeutralCWD != nil, "neutral_cwd")
		check(r.MaxStderrBytes != nil, "max_stderr_bytes")
		check(r.Version != "", "version")
		check(len(r.Semantics) > 0 && string(r.Semantics) != "{}" && string(r.Semantics) != "null", "semantics")
		check(r.RuntimeProfile != "", "runtime_profile")
		check(r.Sandbox != nil, "sandbox")
		if len(forbidden) > 0 {
			return Spec{}, errors.New("repository provider_id configuration may not set trusted fields: " + strings.Join(forbidden, ", "))
		}
		return ResolveTrusted(root, r.ProviderID, r, defaultName)
	}
	if truthyEnv(UnsafeRepoCommandEnv) {
		return r.spec(defaultName)
	}
	return Spec{}, errors.New("repository-defined provider commands are disabled; configure provider_id in the project and register executable authority outside the repository")
}
