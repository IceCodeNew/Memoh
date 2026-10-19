//go:build linux

package bridgesvc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const noFollowResolve = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS

func openNoFollowRoot(root string) (int, error) {
	root, _, err := validateNoFollowPath(root, "placeholder")
	if err != nil {
		return -1, err
	}
	fsRoot, err := unix.Open(string(filepath.Separator), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open filesystem root: %w", err)
	}
	relativeRoot := strings.TrimPrefix(root, string(filepath.Separator))
	if relativeRoot == "" {
		return fsRoot, nil
	}
	defer func() { _ = unix.Close(fsRoot) }()
	fd, err := unix.Openat2(fsRoot, relativeRoot, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: noFollowResolve,
	})
	if err != nil {
		return -1, fmt.Errorf("open nofollow root: %w", err)
	}
	return fd, nil
}

func openRegularNoFollow(root, relativePath string) (*os.File, error) {
	rootFD, err := openNoFollowRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(rootFD) }()
	// Resolve the complete path in one syscall. In particular, do not walk to
	// a parent descriptor first: openat2 can then detect concurrent ancestor
	// renames while enforcing BENEATH and NO_SYMLINKS for the whole lookup.
	fd, err := unix.Openat2(rootFD, relativePath, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK,
		Resolve: noFollowResolve,
	})
	if err != nil {
		return nil, fmt.Errorf("open nofollow file: %w", err)
	}
	if err := requireRegularFD(fd); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), relativePath), nil //nolint:gosec // successful openat2 returns a non-negative file descriptor.
}

func requireRegularFD(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("fstat opened file: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("opened path is not a regular file")
	}
	return nil
}
