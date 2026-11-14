//go:build !windows

package winprint

import "errors"

// New is only implemented on Windows. Elsewhere the agent can still run
// with a fake spooler (tests).
func New() (Spooler, error) {
	return nil, errors.New("друк доступний лише у Windows")
}
