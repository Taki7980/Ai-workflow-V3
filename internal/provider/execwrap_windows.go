//go:build windows

package provider

import (
	"fmt"
	"io"
)

// ExecWithLimits is unavailable on Windows: sandboxing is Linux-only, so a
// policy that needs limits fails closed before this is ever launched.
func ExecWithLimits(_ []string, errOut io.Writer) int {
	fmt.Fprintln(errOut, "resource limits are unavailable on this platform")
	return 126
}
