package common

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"slipdrukka/internal/i18n"
)

// The log file is written in the chosen language; the lines kept in memory
// stay in the program's own language and are translated per viewer.
func TestLogLanguage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.log")
	lang := i18n.EN
	l := NewLogRing(p, 10)
	l.Lang = func() string { return lang }
	l.Printf("точка «%s» відключилась", "Вокзальна")
	lang = i18n.UK
	l.Printf("точка «%s» відключилась", "Вокзальна")
	b, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimSpace(string(b)), "\r\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "shop “Вокзальна” disconnected") || !strings.HasSuffix(lines[1], "точка «Вокзальна» відключилась") {
		t.Fatalf("файл журналу: %q", lines)
	}
	if got := l.Lines()[0]; !strings.HasSuffix(got, "точка «Вокзальна» відключилась") {
		t.Fatalf("у пам'яті має лишатися українською: %q", got)
	}
	if got := l.LinesIn(i18n.EN)[1]; !strings.HasSuffix(got, "shop “Вокзальна” disconnected") {
		t.Fatalf("переклад для панелі: %q", got)
	}
}
