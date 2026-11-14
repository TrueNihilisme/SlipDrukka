package internal_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"net"
	"strconv"
	"testing"
	"time"

	"slipdrukka/internal/agent"
	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/server"
	"slipdrukka/internal/winprint"
)

type noAdmin struct{}

func (noAdmin) Drivers() ([]string, error) { return nil, nil }
func (noAdmin) QueuesByPort() (map[string][]server.Queue, error) {
	return map[string][]server.Queue{}, nil
}
func (noAdmin) CreateQueue(string, string, string, int) error { return nil }
func (noAdmin) RenameQueue(string, string) error              { return nil }

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitFor(t *testing.T, what string, cond func() bool) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("не дочекався: %s", what)
}

// sendRaw does what the Windows "Standard TCP/IP Port" (RAW) does: connect,
// write the job, close.
func sendRaw(port int, data []byte) error {
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.Write(data); err != nil {
		return err
	}
	return c.(*net.TCPConn).CloseWrite()
}

func TestEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- server
	sdir := t.TempDir()
	agentPort := freePort(t)
	firstLocal := freePort(t)
	cfg := map[string]any{"listen": "127.0.0.1:" + strconv.Itoa(agentPort), "web": "127.0.0.1:" + strconv.Itoa(freePort(t)),
		"first_port": firstLocal, "wg_mode": testWGMode}
	if err := common.SaveJSON(sdir+"/server.json", cfg); err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(sdir, common.NewLogRing("", 100), noAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.SetPublicAddr("127.0.0.1:" + strconv.Itoa(agentPort)); err != nil {
		t.Fatal(err)
	}
	go srv.Run(ctx)
	id, code, err := srv.AddAgent("mag1")
	if err != nil {
		t.Fatal(err)
	}

	// --- agent with two fake printers
	spool := &winprint.Fake{Printers: []winprint.PrinterInfo{{Name: "XP-58 (USB001)"}, {Name: "Label 420B"}}}
	adir := t.TempDir()
	if err := common.SaveJSON(adir+"/agent.json", map[string]any{"web": "127.0.0.1:" + strconv.Itoa(freePort(t))}); err != nil {
		t.Fatal(err)
	}
	ag, err := agent.New(adir, common.NewLogRing("", 100), spool)
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.SetShared([]proto.Printer{{Name: "XP-58 (USB001)", Alias: "chek"}, {Name: "Label 420B", Alias: "etiketka"}}); err != nil {
		t.Fatal(err)
	}
	if err := ag.SetCode(code); err != nil {
		t.Fatal(err)
	}
	go ag.Run(ctx)

	waitFor(t, "агент на зв'язку", func() bool { return ag.Status().Connected })
	var ports = map[string]int{}
	waitFor(t, "порти на сервері", func() bool {
		for _, a := range srv.State().Agents {
			if a.ID == id && a.Online {
				for _, p := range a.Printers {
					ports[p.Alias] = p.Port
				}
			}
		}
		return ports["chek"] != 0 && ports["etiketka"] != 0
	})

	// 0) Network view (0.17.0): the shop learns its public address from the
	// server; the server gets the shop's own addresses and round trips.
	if st := ag.Status(); st.SeenAs != "127.0.0.1" || st.ServerIP != "127.0.0.1" {
		t.Fatalf("shop network status: seen_as=%q server_ip=%q", st.SeenAs, st.ServerIP)
	}
	waitFor(t, "мережа і відгук точки на сервері", func() bool {
		for _, a := range srv.State().Agents {
			if a.ID == id && a.SeenAs == "127.0.0.1" && len(a.RTT) > 0 && a.Inventory != nil && a.Inventory.Net != nil &&
				a.Inventory.Net.ServerAddr == "127.0.0.1:"+strconv.Itoa(agentPort) {
				return true
			}
		}
		return false
	})
	if _, err := srv.Trace(ctx, id, "server"); err == nil {
		t.Fatal("trace to a loopback point address must be refused")
	}

	// 1) Byte-exact transfer, including a job larger than one frame.
	big := make([]byte, 3*proto.MaxFrame+12345)
	rand.Read(big)
	receipt := []byte("\x1b@Hello\n\x1dk\x04QR-DATA\x00\n\x1dV\x00") // ESC/POS-ish
	if err := sendRaw(ports["chek"], receipt); err != nil {
		t.Fatal(err)
	}
	if err := sendRaw(ports["etiketka"], big); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "обидва завдання надруковано", func() bool {
		return len(spool.Jobs("XP-58 (USB001)")) == 1 && len(spool.Jobs("Label 420B")) == 1
	})
	if !bytes.Equal(spool.Jobs("XP-58 (USB001)")[0], receipt) {
		t.Fatal("чек дійшов зі змінами")
	}
	if sha256.Sum256(spool.Jobs("Label 420B")[0]) != sha256.Sum256(big) {
		t.Fatal("велике завдання дійшло зі змінами")
	}

	// 2) Point offline: the job is accepted into the server queue (Windows
	// sees success), waits, and is printed exactly once when the point is back.
	newCode, err := srv.Rekey(id) // old code stops working, session is dropped
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "агент відключився", func() bool { return !ag.Status().Connected })
	waitFor(t, "сервер бачить точку офлайн", func() bool { return !srv.State().Agents[0].Online })
	late := []byte("\x1b@OFFLINE RECEIPT\n\x1dV\x00")
	if err := sendRaw(ports["chek"], late); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "завдання в черзі сервера", func() bool { return len(srv.State().Agents[0].Jobs) == 1 })
	if n := len(spool.Jobs("XP-58 (USB001)")); n != 1 {
		t.Fatalf("офлайн-точка щось надрукувала (%d)", n)
	}
	if err := ag.SetCode(newCode); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "чек з черги надруковано після повернення точки", func() bool { return len(spool.Jobs("XP-58 (USB001)")) == 2 })
	if !bytes.Equal(spool.Jobs("XP-58 (USB001)")[1], late) {
		t.Fatal("чек з черги дійшов зі змінами")
	}
	waitFor(t, "черга порожня", func() bool { return len(srv.State().Agents[0].Jobs) == 0 })
	time.Sleep(300 * time.Millisecond)
	if n := len(spool.Jobs("XP-58 (USB001)")); n != 2 {
		t.Fatalf("чек надруковано %d разів замість одного", n-1)
	}
}

func TestCodeRoundTrip(t *testing.T) {
	c := proto.Code{Addr: "203.0.113.10:7310", Key: "k", Fingerprint: "f"}
	got, err := proto.ParseCode("  " + c.String() + "\n")
	if err != nil || got != c {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := proto.ParseCode("garbage"); err == nil {
		t.Fatal("сміття прийнято як код")
	}
}

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{"XP-58 (USB001)": "XP-58-USB001", "  ": "printer", "Чек каса 1": "Чек-каса-1", `a"b'<c>`: "abc"} {
		if got := proto.SanitizeName(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
