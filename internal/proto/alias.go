package proto

import "strings"

// SanitizeName turns a free-form printer or point name into something safe
// for Windows queue/port names and URLs: letters, digits, '-', '_' and '.',
// at most 40 characters.
func SanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ' || r == '/' || r == '\\':
			b.WriteRune('-')
		case r >= 0x0400 && r <= 0x04FF: // Cyrillic stays readable
			b.WriteRune(r)
		}
		if b.Len() >= 40 {
			break
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "printer"
	}
	return out
}
