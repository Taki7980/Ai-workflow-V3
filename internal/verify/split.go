package verify

import (
	"errors"
	"runtime"
	"strings"
)

// SplitCommand splits a check command into argv without invoking a shell.
// Quotes group words everywhere; backslash escapes apply only off Windows,
// matching V2's shlex posix / non-posix split.
func SplitCommand(s string) ([]string, error) {
	return splitCommand(s, runtime.GOOS != "windows")
}

func splitCommand(s string, backslashEscapes bool) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		inWord  bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch {
			case r == '"':
				quote = 0
			case r == '\\' && backslashEscapes:
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\' && backslashEscapes:
			escaped, inWord = true, true
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, errors.New("No closing quotation")
	}
	if escaped {
		return nil, errors.New("No escaped character")
	}
	if inWord {
		args = append(args, cur.String())
	}
	return args, nil
}
