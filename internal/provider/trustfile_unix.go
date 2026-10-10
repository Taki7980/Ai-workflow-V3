//go:build !windows

package provider

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// readTrustedFile opens with O_NOFOLLOW and validates the opened inode:
// regular file, not group/other writable, owned by the current user or root.
func readTrustedFile(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil, errors.New("trusted provider registry not found: " + path)
		}
		return nil, errors.New("trusted provider registry could not be opened safely (symlinks are refused)")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return nil, errors.New("trusted provider registry must be a regular file")
	}
	if st.Mode&0o022 != 0 {
		return nil, errors.New("trusted provider registry may not be writable by group or others")
	}
	if uid := uint32(os.Getuid()); st.Uid != uid && st.Uid != 0 {
		return nil, errors.New("trusted provider registry must be owned by the current user or root")
	}
	return io.ReadAll(io.LimitReader(f, 4<<20))
}
