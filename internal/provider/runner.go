package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	DefaultMaxOutput = 8 << 20
	DefaultMaxStderr = 64 << 10
	ProtocolVersion  = 1
)

type Spec struct {
	Name           string        `json:"name"`
	Command        []string      `json:"command"`
	Timeout        time.Duration `json:"-"`
	MaxOutputBytes int64         `json:"max_output_bytes"`
	MaxStderrBytes int64         `json:"max_stderr_bytes"`
	EnvAllowlist   []string      `json:"env_allowlist"`
}

type Request struct {
	Query  string `json:"query"`
	Root   string `json:"root"`
	Limit  int    `json:"limit"`
	Intent string `json:"intent,omitempty"`
}

type Item struct {
	Text   string  `json:"text"`
	Score  float64 `json:"score,omitempty"`
	Path   string  `json:"path,omitempty"`
	Line   int     `json:"line,omitempty"`
	SHA256 string  `json:"sha256,omitempty"`
}

type Result struct {
	Items      []Item `json:"items"`
	Stderr     string `json:"stderr,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

func (s Spec) validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("provider name is blank")
	}
	if len(s.Command) == 0 || strings.TrimSpace(s.Command[0]) == "" {
		return errors.New("provider command is blank")
	}
	return nil
}

func Run(parent context.Context, s Spec, req Request) (Result, error) {
	if err := s.validate(); err != nil {
		return Result{}, err
	}
	if s.Timeout <= 0 {
		s.Timeout = 8 * time.Second
	}
	if s.MaxOutputBytes <= 0 {
		s.MaxOutputBytes = DefaultMaxOutput
	}
	if s.MaxStderrBytes <= 0 {
		s.MaxStderrBytes = DefaultMaxStderr
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(parent, s.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Command[0], s.Command[1:]...)
	cmd.Dir = req.Root
	cmd.Stdin = bytes.NewReader(append(payload, '\n'))
	cmd.Env = buildEnv(s.EnvAllowlist)
	var stdout, stderr limitedBuffer
	stdout.N = s.MaxOutputBytes
	stderr.N = s.MaxStderrBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("provider %s: %w", s.Name, ctx.Err())
		}
		return Result{}, fmt.Errorf("provider %s failed: %w: %s", s.Name, err, stderr.String())
	}
	items, err := decodeItems(stdout.Bytes())
	if err != nil {
		return Result{}, fmt.Errorf("provider %s output: %w", s.Name, err)
	}
	return Result{Items: items, Stderr: stderr.String(), DurationMS: time.Since(start).Milliseconds()}, nil
}

func buildEnv(allow []string) []string {
	safe := map[string]bool{
		"PATH": true, "PATHEXT": true, "SYSTEMROOT": true, "SYSTEMDRIVE": true,
		"WINDIR": true, "COMSPEC": true, "HOME": true, "USERPROFILE": true,
		"TMP": true, "TEMP": true, "TMPDIR": true, "LANG": true, "LC_ALL": true,
		"LC_CTYPE": true,
	}
	for _, k := range allow {
		safe[k] = true
	}
	out := []string{}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if safe[k] {
			out = append(out, kv)
		}
	}
	return out
}

func decodeItems(b []byte) ([]Item, error) {
	var envelope struct {
		Items []Item `json:"items"`
	}
	if err := json.Unmarshal(b, &envelope); err == nil && envelope.Items != nil {
		return envelope.Items, nil
	}
	var arr []Item
	if err := json.Unmarshal(b, &arr); err == nil {
		return arr, nil
	}
	return nil, errors.New("expected JSON array or {items:[...]}")
}

type limitedBuffer struct {
	buf      bytes.Buffer
	N        int64
	overflow bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.N <= 0 {
		return len(p), nil
	}
	remain := l.N - int64(l.buf.Len())
	if remain <= 0 {
		l.overflow = true
		return len(p), nil
	}
	write := p
	if int64(len(write)) > remain {
		write = write[:remain]
		l.overflow = true
	}
	_, err := l.buf.Write(write)
	return len(p), err
}
func (l *limitedBuffer) Bytes() []byte  { return l.buf.Bytes() }
func (l *limitedBuffer) String() string { return l.buf.String() }

var _ io.Writer = (*limitedBuffer)(nil)
