//go:build linux

package bridgesvc

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// noFollowModes runs every case against the native openat2 lookup and
// against the descriptor walk forced by each errno that marks openat2 as
// unavailable.
var noFollowModes = []struct {
	name  string
	errno unix.Errno // zero keeps the real openat2
}{
	{name: "openat2"},
	{name: "fallback ENOSYS", errno: unix.ENOSYS},
	{name: "fallback EPERM", errno: unix.EPERM},
}

// useNoFollowMode swaps the openat2 seam for the test and returns a counter
// of seam calls, proving which lookup actually ran.
func useNoFollowMode(t *testing.T, errno unix.Errno) *atomic.Int32 {
	t.Helper()
	original := openat2
	t.Cleanup(func() { openat2 = original })
	calls := new(atomic.Int32)
	if errno == 0 {
		probe, err := original(unix.AT_FDCWD, ".", &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC})
		if openat2Unavailable(err) {
			t.Skipf("openat2 is unavailable here: %v", err)
		}
		if err != nil {
			t.Fatalf("probe openat2: %v", err)
		}
		_ = unix.Close(probe)
		openat2 = func(dirfd int, path string, how *unix.OpenHow) (int, error) {
			calls.Add(1)
			return original(dirfd, path, how)
		}
		return calls
	}
	openat2 = func(int, string, *unix.OpenHow) (int, error) {
		calls.Add(1)
		return -1, errno
	}
	return calls
}

type noFollowFixture struct {
	base string
	root string
}

// newNoFollowFixture builds a symlink-free root holding regular files, a
// FIFO, and symlinks in every position the lookup must refuse.
func newNoFollowFixture(t *testing.T) noFollowFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "root")
	for _, dir := range []string{filepath.Join(root, "dir", "sub"), filepath.Join(base, "outside")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(root, "file.txt"):               "hello",
		filepath.Join(root, "dir", "nested.txt"):      "nested",
		filepath.Join(root, "dir", "sub", "deep.txt"): "deep",
		filepath.Join(base, "outside", "secret.txt"):  "secret",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		filepath.Join(root, "link-file"):     "file.txt",
		filepath.Join(root, "link-dangling"): "missing.txt",
		filepath.Join(root, "link-dir"):      "dir",
		filepath.Join(root, "link-outside"):  filepath.Join(base, "outside"),
		filepath.Join(base, "root-alias"):    "root",
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	return noFollowFixture{base: base, root: root}
}

// openNoFollowWithin fails the test instead of hanging when an open blocks,
// which is how a FIFO opened without O_NONBLOCK would misbehave.
func openNoFollowWithin(t *testing.T, root, relativePath string) (*os.File, error) {
	t.Helper()
	type result struct {
		f   *os.File
		err error
	}
	done := make(chan result, 1)
	go func() {
		f, err := openRegularNoFollow(root, relativePath)
		done <- result{f: f, err: err}
	}()
	select {
	case r := <-done:
		return r.f, r.err
	case <-time.After(5 * time.Second):
		t.Fatalf("openRegularNoFollow(%q, %q) blocked", root, relativePath)
		return nil, nil
	}
}

func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("list open descriptors: %v", err)
	}
	return len(entries)
}

type noFollowCase struct {
	name         string
	root         string
	relativePath string
	want         codes.Code
	content      string
}

func noFollowCases(t *testing.T, fx noFollowFixture) []noFollowCase {
	t.Helper()
	cases := []noFollowCase{
		{name: "regular file", root: fx.root, relativePath: "file.txt", want: codes.OK, content: "hello"},
		{name: "nested regular file", root: fx.root, relativePath: "dir/sub/deep.txt", want: codes.OK, content: "deep"},
		{name: "filesystem root as root", root: "/", relativePath: strings.TrimPrefix(filepath.Join(fx.root, "file.txt"), "/"), want: codes.OK, content: "hello"},
		{name: "symlink final component", root: fx.root, relativePath: "link-file", want: codes.FailedPrecondition},
		{name: "dangling symlink final component", root: fx.root, relativePath: "link-dangling", want: codes.FailedPrecondition},
		{name: "symlink intermediate directory", root: fx.root, relativePath: "link-dir/nested.txt", want: codes.FailedPrecondition},
		{name: "symlink intermediate directory escaping root", root: fx.root, relativePath: "link-outside/secret.txt", want: codes.FailedPrecondition},
		{name: "symlink directory with trailing slash", root: fx.root, relativePath: "link-dir/", want: codes.FailedPrecondition},
		{name: "symlink as root", root: filepath.Join(fx.base, "root-alias"), relativePath: "file.txt", want: codes.FailedPrecondition},
		{name: "symlink ancestor of root", root: filepath.Join(fx.base, "root-alias", "dir"), relativePath: "nested.txt", want: codes.FailedPrecondition},
		{name: "fifo", root: fx.root, relativePath: "fifo", want: codes.FailedPrecondition},
		{name: "directory", root: fx.root, relativePath: "dir", want: codes.FailedPrecondition},
		{name: "regular file as intermediate directory", root: fx.root, relativePath: "file.txt/nested.txt", want: codes.FailedPrecondition},
		{name: "missing file", root: fx.root, relativePath: "missing.txt", want: codes.NotFound},
		{name: "missing intermediate directory", root: fx.root, relativePath: "absent/file.txt", want: codes.NotFound},
		{name: "missing root", root: filepath.Join(fx.base, "absent"), relativePath: "file.txt", want: codes.NotFound},
		// Callers only pass validated traversal-free paths; these confirm the
		// lookup itself still refuses to leave the root if one slips through.
		{name: "parent traversal", root: fx.root, relativePath: "../outside/secret.txt", want: codes.FailedPrecondition},
		{name: "absolute relative path", root: fx.root, relativePath: filepath.Join(fx.base, "outside", "secret.txt"), want: codes.FailedPrecondition},
	}
	procFD := fmt.Sprintf("/proc/%d/fd", os.Getpid())
	if _, err := os.Stat(procFD); err != nil {
		t.Logf("skipping magic link cases: %v", err)
		return cases
	}
	held, err := os.Open(filepath.Join(fx.root, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	return append(cases,
		noFollowCase{name: "magic link in root path", root: fmt.Sprintf("/proc/%d/root", os.Getpid()), relativePath: strings.TrimPrefix(filepath.Join(fx.root, "file.txt"), "/"), want: codes.FailedPrecondition},
		noFollowCase{name: "magic link final component", root: procFD, relativePath: strconv.FormatUint(uint64(held.Fd()), 10), want: codes.FailedPrecondition},
	)
}

func TestOpenRegularNoFollow(t *testing.T) {
	for _, mode := range noFollowModes {
		t.Run(mode.name, func(t *testing.T) {
			calls := useNoFollowMode(t, mode.errno)
			fx := newNoFollowFixture(t)
			cases := noFollowCases(t, fx)
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					f, err := openNoFollowWithin(t, tc.root, tc.relativePath)
					if f != nil {
						defer func() { _ = f.Close() }()
					}
					if got := status.Code(noFollowOpenStatus("open", err)); got != tc.want {
						t.Fatalf("openRegularNoFollow(%q, %q) error = %v, code %s, want %s", tc.root, tc.relativePath, err, got, tc.want)
					}
					if tc.want != codes.OK {
						t.Logf("rejected: %v", err)
						return
					}
					data, err := io.ReadAll(f)
					if err != nil {
						t.Fatalf("read: %v", err)
					}
					if string(data) != tc.content {
						t.Fatalf("content = %q, want %q", data, tc.content)
					}
				})
			}
			if calls.Load() == 0 {
				t.Fatal("openat2 seam was never called")
			}

			// Every intermediate and rejected descriptor must be closed. The
			// second pass opens without a timeout, so skip it after a failure
			// such as a blocking FIFO.
			if t.Failed() {
				return
			}
			before := countOpenFDs(t)
			for _, tc := range cases {
				if f, err := openRegularNoFollow(tc.root, tc.relativePath); err == nil {
					_ = f.Close()
				}
			}
			if after := countOpenFDs(t); after != before {
				t.Fatalf("open descriptors = %d after lookups, want %d", after, before)
			}
		})
	}
}

func TestWalkNoFollowRejectsUncleanComponents(t *testing.T) {
	useNoFollowMode(t, unix.ENOSYS)
	fx := newNoFollowFixture(t)
	for _, relativePath := range []string{"./file.txt", "dir/../file.txt", "dir/./nested.txt", "dir//nested.txt", "dir/", ""} {
		_, err := openRegularNoFollow(fx.root, relativePath)
		if got := status.Code(noFollowOpenStatus("open", err)); got != codes.FailedPrecondition {
			t.Errorf("openRegularNoFollow(%q, %q) error = %v, code %s, want FailedPrecondition", fx.root, relativePath, err, got)
		}
	}
}
