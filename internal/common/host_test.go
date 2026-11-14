package common

import (
	"os"
	"strings"
	"testing"
)

func TestHostAllowed(t *testing.T) {
	me, _ := os.Hostname()
	ok := []string{"127.0.0.1:8780", "localhost:8781", "[::1]:8780", "192.168.10.20:8780", "LOCALHOST", strings.ToUpper(me) + ":8780"}
	for _, h := range ok {
		if !HostAllowed(h) {
			t.Errorf("%q мав пройти", h)
		}
	}
	bad := []string{"evil.example.com:8781", "slipdrukka.attacker.net", "localhost.evil.com:8780", ""}
	for _, h := range bad {
		if HostAllowed(h) {
			t.Errorf("%q мав бути відхилений", h)
		}
	}
}
