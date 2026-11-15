package tlsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The certificate is made once and then only read; damaged or half-missing
// files are an error and stay as they are.
func TestLoadOrCreate(t *testing.T) {
	dir := t.TempDir()
	c1, created, err := LoadOrCreate(dir)
	if err != nil || !created {
		t.Fatalf("first run: created=%v err=%v", created, err)
	}
	c2, created, err := LoadOrCreate(dir)
	if err != nil || created {
		t.Fatalf("second run: created=%v err=%v", created, err)
	}
	if Fingerprint(c1) != Fingerprint(c2) {
		t.Fatal("the certificate changed between runs")
	}

	crt := filepath.Join(dir, "server.crt")
	good, err := os.ReadFile(crt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(crt, good[:len(good)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreate(dir); !errors.Is(err, ErrDamaged) {
		t.Fatalf("damaged file: %v", err)
	}
	if b, _ := os.ReadFile(crt); len(b) != len(good)/2 {
		t.Fatal("the damaged file was overwritten")
	}

	if err := os.Remove(crt); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreate(dir); !errors.Is(err, ErrDamaged) {
		t.Fatalf("one file missing: %v", err)
	}
	if _, err := os.Stat(crt); !os.IsNotExist(err) {
		t.Fatal("a certificate was made next to the old key")
	}
}
