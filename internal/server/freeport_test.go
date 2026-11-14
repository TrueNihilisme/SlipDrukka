package server

import (
	"strings"
	"testing"

	"slipdrukka/internal/common"
)

type queueAdmin struct{ nopAdmin }

func (queueAdmin) QueuesByPort() (map[string][]Queue, error) {
	return map[string][]Queue{PortName(9101): {{Name: "Чеки каса 1", Driver: "XP-80C"}}}, nil
}

// A port still used by a Windows queue on the server must not be freed:
// its number would go to another printer and the queue would print there.
func TestFreePortKeepsUsedPort(t *testing.T) {
	s, err := New(t.TempDir(), common.NewLogRing("", 10), queueAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Ports["a1/labels"] = 9101
	err = s.FreePort("a1", "labels")
	if err == nil || !strings.Contains(err.Error(), "Чеки каса 1") {
		t.Fatalf("мало відмовити з назвою черги: %v", err)
	}
	if _, ok := s.cfg.Ports["a1/labels"]; !ok {
		t.Fatal("порт зник попри відмову")
	}
}
