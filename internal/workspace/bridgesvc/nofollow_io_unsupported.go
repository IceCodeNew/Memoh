//go:build !linux

package bridgesvc

import "os"

func openRegularNoFollow(_, _ string) (*os.File, error) {
	return nil, errNoFollowUnsupported
}
