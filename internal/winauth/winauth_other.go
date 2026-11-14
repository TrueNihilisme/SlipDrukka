//go:build !windows

package winauth

import "errors"

func CheckAdmin(string, string) error {
	return errors.New("вхід через Windows доступний лише у Windows")
}
func Supported() bool { return false }
