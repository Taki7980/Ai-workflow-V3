package verify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode"
)

// Compress bounds noisy text exactly like V2 compress.compress_text: keep 75%
// head and the remaining tail lines, then apply a code-point character cap.
func Compress(text string, maxLines, maxChars int) string {
	lines := splitLines(text)
	if len(lines) > maxLines {
		head := max(1, int(float64(maxLines)*0.75))
		tail := maxLines - head
		kept := append([]string{}, lines[:head]...)
		kept = append(kept, fmt.Sprintf("... [%d LINES OMITTED] ...", len(lines)-maxLines))
		// V2 slices lines[-tail:]; Python's lines[-0:] is the whole list, so
		// tail == 0 (maxLines == 1) re-appends every line. Mirrored for parity.
		start := len(lines) - tail
		if tail == 0 {
			start = 0
		}
		lines = append(kept, lines[start:]...)
	}
	out := strings.Join(lines, "\n")
	if r := []rune(out); len(r) > maxChars {
		out = strings.TrimRightFunc(string(r[:max(0, maxChars-40)]), unicode.IsSpace) + "\n... [CHARACTER CAP REACHED]"
	}
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

// CompressPreferRTK pipes text through `rtk pipe` when available and falls
// back to Compress on any failure or blank output.
func CompressPreferRTK(text string, maxLines, maxChars int, filter string) string {
	if path, err := exec.LookPath("rtk"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		args := []string{"pipe"}
		if filter != "" {
			args = append(args, "--filter", filter)
		}
		cmd := exec.CommandContext(ctx, path, args...)
		cmd.Stdin = strings.NewReader(text)
		if out, err := cmd.Output(); err == nil && strings.TrimSpace(string(out)) != "" {
			return string(out)
		}
	}
	return Compress(text, maxLines, maxChars)
}

// splitLines mirrors Python str.splitlines() for \n, \r\n and \r.
func splitLines(s string) []string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
