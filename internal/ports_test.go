package internal_test

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"slipdrukka/internal/agent"
	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/server"
	"slipdrukka/internal/winprint"
)

func httpOK(port int) bool {
	c := http.Client{Timeout: 2 * time.Second}
	r, err := c.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
	if err != nil {
		return false
	}
	r.Body.Close()
	return r.StatusCode == 200
}

func refused(port int) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		return true
	}
	c.Close()
	return false
}

func TestPortMoves(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sdir := t.TempDir()
	agentPort, webPort, first := freePort(t), freePort(t), freePort(t)
	common.SaveJSON(sdir+"/server.json", map[string]any{"listen": "127.0.0.1:" + strconv.Itoa(agentPort),
		"web": "127.0.0.1:" + strconv.Itoa(webPort), "first_port": first, "wg_mode": testWGMode})
	srv, err := server.New(sdir, common.NewLogRing("", 100), noAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	var fw []string
	srv.FirewallAdd = func(name, protocol string, port int, _ []string) error {
		if protocol != "TCP" {
			return nil // the WireGuard rule is checked in the server package
		}
		fw = append(fw, name+":"+strconv.Itoa(port))
		return nil
	}
	srv.FirewallRemove = func(string) {}
	srv.SetPublicAddr("127.0.0.1:" + strconv.Itoa(agentPort))
	go srv.Run(ctx)
	_, code, _ := srv.AddAgent("p1")
	waitFor(t, "панель сервера", func() bool { return httpOK(webPort) })

	spool := &winprint.Fake{Printers: []winprint.PrinterInfo{{Name: "P"}}}
	adir := t.TempDir()
	agentWeb := freePort(t)
	common.SaveJSON(adir+"/agent.json", map[string]any{"web": "127.0.0.1:" + strconv.Itoa(agentWeb)})
	ag, _ := agent.New(adir, common.NewLogRing("", 100), spool)
	var published string
	ag.OnPanel = func(u string) error { published = u; return nil }
	ag.SetShared([]proto.Printer{{Name: "P", Alias: "p"}})
	ag.SetCode(code)
	go ag.Run(ctx)
	waitFor(t, "агент на зв'язку", func() bool { return ag.Status().Connected })

	// Busy port: nothing changes.
	busy, _ := net.Listen("tcp", ":0")
	busyPort := busy.Addr().(*net.TCPAddr).Port
	if err := srv.SetPorts(server.Ports{AgentPort: busyPort, WebPort: webPort, FirstPort: first}); err == nil {
		t.Fatal("зайнятий порт прийнято")
	}
	busy.Close()
	if refused(agentPort) {
		t.Fatal("після невдалої зміни старий порт для точок закрився")
	}

	// Real move of both server ports.
	newAgent, newWeb := freePort(t), freePort(t)
	if err := srv.SetPorts(server.Ports{AgentPort: newAgent, WebPort: newWeb, FirstPort: first}); err != nil {
		t.Fatal(err)
	}
	if !httpOK(newWeb) {
		t.Fatal("панель не відкрилась на новому порту")
	}
	waitFor(t, "старий порт панелі закрився", func() bool { return refused(webPort) })
	if !refused(agentPort) {
		t.Fatal("старий порт для точок досі відкритий")
	}
	if !ag.Status().Connected {
		t.Fatal("переїзд порту обірвав уже підключену точку")
	}
	// fw[0] is the rule applied at start; the move must add the new port last.
	if len(fw) < 2 || fw[0] != "SlipDrukka Server:"+strconv.Itoa(agentPort) || fw[len(fw)-1] != "SlipDrukka Server:"+strconv.Itoa(newAgent) {
		t.Fatalf("правило брандмауера не оновлено: %v", fw)
	}

	// The point reconnects to the new port with a new code and prints.
	srv.SetPublicAddr("127.0.0.1:" + strconv.Itoa(newAgent))
	newCode, _ := srv.Rekey(srv.State().Agents[0].ID)
	ag.SetCode(newCode)
	waitFor(t, "агент на новому порту", func() bool { return ag.Status().Connected })
	port := srv.State().Agents[0].Printers[0].Port
	if err := sendRaw(port, []byte("after-move")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "друк після переїзду", func() bool { return len(spool.Jobs("P")) == 1 })

	// The point's page stays where it is and the tray icon knows the address.
	if !httpOK(agentWeb) {
		t.Fatal("сторінка точки не відповідає")
	}
	if published != "http://127.0.0.1:"+strconv.Itoa(agentWeb)+"/" {
		t.Fatalf("значок не дізнався адресу сторінки: %q", published)
	}
}
