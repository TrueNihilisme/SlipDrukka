package winsvc

import (
	"errors"
	"regexp"
	"strings"
)

// Print security descriptors are edited as SDDL text: the discretionary ACL
// is taken apart into its entries, the entries of one account are replaced,
// and the result is written back. Anything that does not parse exactly is
// refused rather than guessed at.

var (
	daclRe = regexp.MustCompile(`D:([A-Z]*)((?:\([^()]*\))*)`)
	aceRe  = regexp.MustCompile(`\([^()]*\)`)
)

// daclWith returns the DACL of sddl ("D:<flags><entries>") with every entry
// of sid removed and add appended. sid is a SID string (S-1-...); add holds
// complete entries such as "(A;;0x20003;;;S-1-...)".
func daclWith(sddl, sid string, add []string) (string, error) {
	// Owner and group come first ("O:..G:.."); the DACL is the first "D:".
	i := strings.Index(sddl, "D:")
	if i < 0 {
		return "", errors.New("у дескрипторі безпеки немає списку доступу")
	}
	rest := sddl[i:]
	if j := strings.Index(rest, "S:"); j >= 0 { // audit list, not ours to touch
		rest = rest[:j]
	}
	m := daclRe.FindStringSubmatch(rest)
	if m == nil || m[0] != rest {
		return "", errors.New("список доступу має незвичний вигляд — не змінюю")
	}
	var b strings.Builder
	b.WriteString("D:" + m[1])
	for _, ace := range aceRe.FindAllString(m[2], -1) {
		fields := strings.Split(strings.Trim(ace, "()"), ";")
		if len(fields) != 6 {
			return "", errors.New("запис списку доступу має незвичний вигляд — не змінюю")
		}
		if strings.EqualFold(fields[5], sid) {
			continue
		}
		b.WriteString(ace)
	}
	for _, ace := range add {
		b.WriteString(ace)
	}
	return b.String(), nil
}
