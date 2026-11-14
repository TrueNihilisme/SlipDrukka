package i18n

import "golang.org/x/sys/windows"

// System is the Windows display language (Ukrainian → UK, anything else → EN).
func System() string {
	const muiLanguageName = 0x8
	langs, err := windows.GetSystemPreferredUILanguages(muiLanguageName)
	if err != nil || len(langs) == 0 {
		return EN
	}
	return Normalize(langs[0])
}
