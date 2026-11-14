package internal_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"slipdrukka/internal/agent"
	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/server"
	"slipdrukka/internal/winprint"
)

func TestParseAllowList(t *testing.T) {
	got, err := server.ParseAllowList("203.0.113.10, 198.51.100.7/24\n203.0.113.10 ::ffff:192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	want := "203.0.113.10,198.51.100.0/24,192.0.2.1"
	if strings.Join(got, ",") != want {
		t.Fatalf("got %v want %s", got, want)
	}
	for _, bad := range []string{"0.0.0.0/0", "::/0", "hello", "300.1.1.1", "10.0.0.1/40"} {
		if _, err := server.ParseAllowList(bad); err == nil {
			t.Fatalf("прийнято %q", bad)
		}
	}
	if l, err := server.ParseAllowList("  "); err != nil || len(l) != 0 {
		t.Fatal("порожній список має означати «будь-яка адреса»")
	}
}

func TestAllowList(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sdir := t.TempDir()
	agentPort := freePort(t)
	common.SaveJSON(sdir+"/server.json", map[string]any{"listen": "127.0.0.1:" + strconv.Itoa(agentPort),
		"web": "127.0.0.1:" + strconv.Itoa(freePort(t)), "first_port": freePort(t), "wg_mode": testWGMode})
	srv, _ := server.New(sdir, common.NewLogRing("", 50), noAdmin{})
	var lastRemotes []string
	srv.FirewallAdd = func(name, protocol string, port int, remote []string) error {
		if protocol == "TCP" && port == agentPort {
			lastRemotes = remote
		}
		return nil
	}
	srv.SetPublicAddr("127.0.0.1:" + strconv.Itoa(agentPort))
	go srv.Run(ctx)
	id, code, _ := srv.AddAgent("shop")

	spool := &winprint.Fake{Printers: []winprint.PrinterInfo{{Name: "P"}}}
	adir := t.TempDir()
	common.SaveJSON(adir+"/agent.json", map[string]any{"web": "127.0.0.1:" + strconv.Itoa(freePort(t))})
	ag, _ := agent.New(adir, common.NewLogRing("", 50), spool)
	ag.SetShared([]proto.Printer{{Name: "P", Alias: "p"}})
	ag.SetCode(code)
	go ag.Run(ctx)
	online := func() bool { return srv.State().Agents[0].Online }
	waitFor(t, "точка на зв'язку", online)

	// 1) A list that would cut the connected point is refused without force.
	err := srv.SetAllowed("10.9.9.9", false)
	var wd *server.WouldDropError
	if !errors.As(err, &wd) || len(wd.Points) != 1 || !strings.Contains(wd.Points[0], "127.0.0.1") {
		t.Fatalf("мало попередити про відключення точки, отримано: %v", err)
	}
	if !online() || len(srv.State().Allowed) != 0 {
		t.Fatal("без підтвердження нічого не мало змінитися")
	}

	// 2) With force: point dropped, cannot come back, shows up as refused,
	// firewall rule narrowed to the list.
	if err := srv.SetAllowed("10.9.9.9", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "точку відключено", func() bool { return !online() })
	reconnect(t, ag, adir) // reconnect right away
	waitFor(t, "адреса у відхилених", func() bool {
		for _, r := range srv.State().Refused {
			if r.IP == "127.0.0.1" {
				return true
			}
		}
		return false
	})
	if online() {
		t.Fatal("точка з недозволеної адреси підключилась")
	}
	if strings.Join(lastRemotes, ",") != "10.9.9.9" {
		t.Fatalf("правило брандмауера: %v", lastRemotes)
	}

	// 3) Allow it again: the point returns.
	if err := srv.SetAllowed("10.9.9.9\n127.0.0.1", false); err != nil {
		t.Fatal(err)
	}
	reconnect(t, ag, adir)
	waitFor(t, "точка повернулась", online)

	// 4) Tie the point's code to another address: refused after hello.
	if err := srv.SetAllowed("", false); err != nil { // global: any
		t.Fatal(err)
	}
	if lastRemotes != nil {
		t.Fatalf("порожній список має відкрити порт для всіх: %v", lastRemotes)
	}
	if err := srv.SetAgentAllowed(id, "127.0.0.2"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "прив'язана точка відключена", func() bool { return !online() })
	reconnect(t, ag, adir)
	waitFor(t, "агент бачить відмову", func() bool { return strings.Contains(ag.Status().Error, "не дозволено") })
	if err := srv.SetAgentAllowed(id, ""); err != nil {
		t.Fatal(err)
	}
	reconnect(t, ag, adir)
	waitFor(t, "точка знову на зв'язку", online)
}
