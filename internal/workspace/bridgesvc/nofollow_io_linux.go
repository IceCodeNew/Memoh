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

// openat2 is a seam so tests can force the descriptor-walk fallback on a
// kernel that supports openat2.
var openat2 = unix.Openat2

// openat2Unavailable reports failures that mean the syscall itself is
// missing (Linux < 5.6) or filtered (older seccomp profiles answer ENOSYS
// or EPERM), rather than a lookup that openat2 rejected.
func openat2Unavailable(err error) bool {
	return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM)
}

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
	fd, err := openat2(fsRoot, relativeRoot, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: noFollowResolve,
	})
	if err != nil {
		return -1, fmt.Errorf("open nofollow root: %w", err)
	}
	return fd, nil
}

func openRegularNoFollow(root, relativePath string) (*os.File, error) {
	fd, err := openat2NoFollow(root, relativePath)
	if openat2Unavailable(err) {
		fd, err = walkNoFollow(root, relativePath)
	}
	if err != nil {
		return nil, err
	}
	if err := requireRegularFD(fd); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), relativePath), nil //nolint:gosec // successful open returns a non-negative file descriptor.
}

func openat2NoFollow(root, relativePath string) (int, error) {
	rootFD, err := openNoFollowRoot(root)
	if err != nil {
		return -1, err
	}
	defer func() { _ = unix.Close(rootFD) }()
	// Resolve the complete path in one syscall. In particular, do not walk to
	// a parent descriptor first: openat2 can then detect concurrent ancestor
	// renames while enforcing BENEATH and NO_SYMLINKS for the whole lookup.
	fd, err := openat2(rootFD, relativePath, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK,
		Resolve: noFollowResolve,
	})
	if err != nil {
		return -1, fmt.Errorf("open nofollow file: %w", err)
	}
	return fd, nil
}

// walkNoFollow opens root/relativePath one component at a time for kernels
// without a usable openat2. Each component is resolved relative to a
// directory descriptor already held, always with O_NOFOLLOW, and "", "." and
// ".." are rejected, so no step can follow a symbolic link (including a
// magic link) or climb out of the directory the previous step reached. A
// symlinked directory fails with ENOTDIR or ELOOP and a symlinked final
// component with ELOOP; both stay fail-closed rather than NotFound.
func walkNoFollow(root, relativePath string) (int, error) {
	root, _, err := validateNoFollowPath(root, "placeholder")
	if err != nil {
		return -1, err
	}
	var rootComponents []string
	if relativeRoot := strings.TrimPrefix(root, string(filepath.Separator)); relativeRoot != "" {
		rootComponents = noFollowComponents(relativeRoot)
	}
	fileComponents := noFollowComponents(relativePath)
	if err := checkNoFollowComponents(rootComponents); err != nil {
		return -1, err
	}
	if err := checkNoFollowComponents(fileComponents); err != nil {
		return -1, err
	}

	dirFD, err := unix.Open(string(filepath.Separator), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open filesystem root: %w", err)
	}
	// The closure releases whichever directory descriptor is current.
	defer func() { _ = unix.Close(dirFD) }()
	descend := func(component, operation string) error {
		next, err := unix.Openat(dirFD, component, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("%s: %w", operation, err)
		}
		_ = unix.Close(dirFD)
		dirFD = next
		return nil
	}
	for _, component := range rootComponents {
		if err := descend(component, "open nofollow root"); err != nil {
			return -1, err
		}
	}
	last := len(fileComponents) - 1
	for _, component := range fileComponents[:last] {
		if err := descend(component, "open nofollow file"); err != nil {
			return -1, err
		}
	}
	fd, err := unix.Openat(dirFD, fileComponents[last], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, fmt.Errorf("open nofollow file: %w", err)
	}
	return fd, nil
}

// noFollowComponents splits a relative path without cleaning it, so empty,
// "." and ".." components (and a leading separator) survive to be rejected.
func noFollowComponents(path string) []string {
	return strings.Split(path, string(filepath.Separator))
}

func checkNoFollowComponents(components []string) error {
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("unsafe path component %q", component)
		}
	}
	return nil
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
