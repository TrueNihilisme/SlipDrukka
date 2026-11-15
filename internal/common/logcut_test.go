package common

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A log file past its size limit is cut to its newer half, and the cut
// falls on a line boundary.
func TestLogCutOnLineBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	line := "2026-01-01 00:00:00 " + strings.Repeat("я", 40) + "\r\n"
	n := maxFile/len(line) + 100
	if err := os.WriteFile(path, bytes.Repeat([]byte(line), n), 0o600); err != nil {
		t.Fatal(err)
	}
	l := NewLogRing(path, 10)
	l.Printf("останній рядок")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxFile*2/3 || len(b) < maxFile/3 {
		t.Fatalf("після обрізання %d байт", len(b))
	}
	if !bytes.HasPrefix(b, []byte("2026-01-01 00:00:00 ")) {
		t.Fatalf("файл починається не з початку рядка: %q", b[:40])
	}
	if !bytes.Contains(b[len(b)-200:], []byte("останній рядок")) {
		t.Fatal("новий рядок не дописано")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("лишився тимчасовий файл")
	}
}
