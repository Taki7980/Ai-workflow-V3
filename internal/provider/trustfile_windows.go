//go:build windows

package provider

import (
	"errors"
	"io"
	"os"
)

// readTrustedFile refuses symlinks/reparse points and non-regular files.
// shortcut: Windows ACL ownership is not checked; the registry lives under the
// per-user LOCALAPPDATA profile. Add an ACL check if shared machines matter.
func readTrustedFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("trusted provider registry not found: " + path)
	}
	if fi.Mode()&os.ModeSymlink != 0 || fi.Mode()&os.ModeIrregular != 0 {
		return nil, errors.New("trusted provider registry may not be a symlink")
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("trusted provider registry must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("trusted provider registry could not be read")
	}
	defer f.Close()
	if after, err := f.Stat(); err != nil || !os.SameFile(fi, after) {
		return nil, errors.New("trusted provider registry changed while opening")
	}
	return io.ReadAll(io.LimitReader(f, 4<<20))
}
