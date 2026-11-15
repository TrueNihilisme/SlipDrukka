package server

import (
	"errors"
	"strings"
	"testing"

	"slipdrukka/internal/common"
	"slipdrukka/internal/winprint"
)

// paperAdmin has one queue on a port of ours and remembers what was set.
type paperAdmin struct {
	nopAdmin
	set    *[]string
	reject bool
}

func (paperAdmin) QueuesByPort() (map[string][]Queue, error) {
	return map[string][]Queue{PortName(9101): {{Name: "Ринок Етикетки", Driver: "Xprinter XP-420B"}}}, nil
}

func (paperAdmin) QueuePaper(string) (winprint.Paper, error) {
	return winprint.Paper{Kind: 256, Width: 800, Length: 1300}, nil
}

func (a paperAdmin) SetQueuePaper(queue string, w, l int) (winprint.Paper, error) {
	if a.reject {
		return winprint.Paper{}, winprint.ErrPaperRejected
	}
	*a.set = append(*a.set, queue)
	return winprint.Paper{Kind: 256, Width: w, Length: l}, nil
}

// The paper size for everyone is written only to printers on this program's
// ports, in tenths of a millimetre, within sane limits; what the driver
// refuses comes back as a plain message.
func TestSetQueuePaper(t *testing.T) {
	var set []string
	s, err := New(t.TempDir(), common.NewLogRing("", 10), paperAdmin{set: &set})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SetQueuePaper("Ринок Етикетки", 40, 25.5)
	if err != nil || got.Width != 40 || got.Length != 25.5 {
		t.Fatalf("розмір 40 × 25.5: %+v %v", got, err)
	}
	if len(set) != 1 || set[0] != "Ринок Етикетки" {
		t.Fatalf("записано в: %v", set)
	}
	q, _ := s.queues()
	if p := q[PortName(9101)][0].Paper; p == nil || p.Width != 800 {
		t.Fatalf("панель не бачить розміру черги: %+v", p)
	}
	if _, err := s.SetQueuePaper("Чужий принтер", 40, 25); err == nil {
		t.Fatal("розмір записано принтеру не на нашому порту")
	}
	for _, bad := range [][2]float64{{0, 25}, {40, -1}, {4.9, 25}, {40, 1000.1}} {
		if _, err := s.SetQueuePaper("Ринок Етикетки", bad[0], bad[1]); err == nil {
			t.Fatalf("розмір %v прийнято", bad)
		}
	}
	if len(set) != 1 {
		t.Fatalf("після відмов записів: %d", len(set))
	}

	s2, err := New(t.TempDir(), common.NewLogRing("", 10), paperAdmin{set: &set, reject: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s2.SetQueuePaper("Ринок Етикетки", 40, 25)
	if err == nil || errors.Is(err, winprint.ErrPaperRejected) || !strings.Contains(err.Error(), "драйвер") {
		t.Fatalf("відмова драйвера: %v", err)
	}
}

// The reset command names the queue as PowerShell text, whatever is in the
// name, and is given only for printers on this program's ports.
func TestPaperResetCommand(t *testing.T) {
	var set []string
	s, err := New(t.TempDir(), common.NewLogRing("", 10), paperAdmin{set: &set})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := s.PaperResetCommand("Ринок Етикетки")
	if err != nil || !strings.HasPrefix(cmd, "$q = 'Ринок Етикетки'\n") {
		t.Fatalf("команда: %q %v", cmd, err)
	}
	if _, err := s.PaperResetCommand("Чужий принтер"); err == nil {
		t.Fatal("команду видано для принтера не на нашому порту")
	}
	if got := paperResetCommand("it's; Remove-Item C:\\ -Recurse #"); !strings.HasPrefix(got, "$q = 'it''s; Remove-Item C:\\ -Recurse #'\n") {
		t.Fatalf("назва не взята в лапки: %q", got)
	}
}
