// Package i18n translates the program's messages. The code keeps writing
// its messages in Ukrainian; they are translated on the way out — log files
// in the language chosen in the settings, the panels in the viewer's
// language. en.json maps every Ukrainian message (a fmt format, or a fixed
// text) to its English form; a formatted message is recognised by turning
// its format into a pattern, and the values in it are translated too when
// they are messages themselves.
package i18n

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	UK = "uk"
	EN = "en"
)

//go:embed en.json
var enJSON []byte

type entry struct {
	re   *regexp.Regexp
	en   string // English format with every verb turned into %s
	lits int    // length of the literal text, longer patterns are tried first
}

var (
	once    sync.Once
	exact   map[string]string
	entries []entry

	cacheMu sync.Mutex
	cache   = map[string]string{}
)

var verb = regexp.MustCompile(`%[-+# 0]*\d*(?:\.\d+)?[sdvqwx%]`)

func load() {
	var m map[string]string
	if err := json.Unmarshal(enJSON, &m); err != nil {
		panic("i18n: en.json: " + err.Error())
	}
	exact = map[string]string{}
	for uk, en := range m {
		if !verb.MatchString(uk) {
			exact[uk] = en
			continue
		}
		var b strings.Builder
		b.WriteString("^")
		lits := 0
		last := 0
		for _, loc := range verb.FindAllStringIndex(uk, -1) {
			lit := uk[last:loc[0]]
			lits += len(lit)
			b.WriteString(regexp.QuoteMeta(lit))
			if uk[loc[0]:loc[1]] == "%%" {
				b.WriteString("%")
			} else {
				b.WriteString("(.*?)")
			}
			last = loc[1]
		}
		lits += len(uk[last:])
		b.WriteString(regexp.QuoteMeta(uk[last:]))
		b.WriteString("$")
		re, err := regexp.Compile(b.String())
		if err != nil {
			panic("i18n: " + uk + ": " + err.Error())
		}
		enFmt := verb.ReplaceAllStringFunc(en, func(v string) string {
			if v == "%%" {
				return "%%"
			}
			return "%s"
		})
		entries = append(entries, entry{re: re, en: enFmt, lits: lits})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].lits > entries[j].lits })
}

var cyr = regexp.MustCompile(`[А-Яа-яІіЇїЄєҐґ]`)

// Tr returns s in lang. Unknown messages come back unchanged.
func Tr(lang, s string) string {
	if lang != EN || s == "" || !cyr.MatchString(s) {
		return s
	}
	once.Do(load)
	cacheMu.Lock()
	if v, ok := cache[s]; ok {
		cacheMu.Unlock()
		return v
	}
	cacheMu.Unlock()
	v := translate(s, 0)
	cacheMu.Lock()
	if len(cache) > 20000 {
		cache = map[string]string{}
	}
	cache[s] = v
	cacheMu.Unlock()
	return v
}

func translate(s string, depth int) string {
	if v, ok := exact[s]; ok {
		return v
	}
	if depth > 3 {
		return s
	}
	for _, e := range entries {
		m := e.re.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		args := make([]any, 0, len(m)-1)
		for _, a := range m[1:] {
			if cyr.MatchString(a) {
				a = translate(a, depth+1)
			}
			args = append(args, a)
		}
		return sprintf(e.en, args)
	}
	return s
}

// sprintf fills %s in order; the formats come from en.json, never from input.
func sprintf(f string, args []any) string {
	var b strings.Builder
	n := 0
	for i := 0; i < len(f); i++ {
		if f[i] == '%' && i+1 < len(f) {
			switch f[i+1] {
			case 's':
				if n < len(args) {
					b.WriteString(args[n].(string))
				}
				n++
				i++
				continue
			case '%':
				b.WriteByte('%')
				i++
				continue
			}
		}
		b.WriteByte(f[i])
	}
	return b.String()
}

// Known reports whether s is a key of the catalog (for tests).
func Known(s string) bool {
	once.Do(load)
	if _, ok := exact[s]; ok {
		return true
	}
	var m map[string]string
	json.Unmarshal(enJSON, &m)
	_, ok := m[s]
	return ok
}

// Keys returns every Ukrainian key (for tests).
func Keys() []string {
	var m map[string]string
	json.Unmarshal(enJSON, &m)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Normalize maps a language tag (uk-UA, en-US…) to UK or EN.
func Normalize(tag string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(tag)), "uk") {
		return UK
	}
	return EN
}

func jsonUnmarshal(v any) error { return json.Unmarshal(enJSON, v) }
