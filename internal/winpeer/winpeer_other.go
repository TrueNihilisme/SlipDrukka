//go:build !windows

package winpeer

// Session is only available on Windows.
func Session(peerPort, localPort int) (session, pid uint32, err error) {
	return 0, 0, ErrUnavailable
}
