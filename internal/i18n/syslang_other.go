//go:build !windows

package i18n

import "os"

// System is the language of the environment (Ukrainian → UK, else EN).
func System() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return Normalize(v)
		}
	}
	return EN
}
