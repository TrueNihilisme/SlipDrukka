package internal_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"slipdrukka/internal/agent"
	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/server"
	"slipdrukka/internal/winprint"
	"slipdrukka/internal/winps"
)

// shopAdmin changes printers of the fake spooler like Windows would.
type shopAdmin struct{ spool *winprint.Fake }

func (shopAdmin) Drivers() ([]string, error)                          { return nil, nil }
func (shopAdmin) AddNetworkPrinter(string, string, int, string) error { return nil }
func (a shopAdmin) AddPrinterOnPort(name, port, driver string) error {
	list, _ := a.spool.List()
	a.spool.Set(append(list, winprint.PrinterInfo{Name: name, Driver: driver, Port: port}))
	return nil
}
func (shopAdmin) RenamePrinter(string, string) error { return nil }
func (a shopAdmin) RemovePrinter(name string) error {
	list, _ := a.spool.List()
	var keep []winprint.PrinterInfo
	for _, p := range list {
		if p.Name != name {
			keep = append(keep, p)
		}
	}
	a.spool.Set(keep)
	return nil
}
func (a shopAdmin) SetPrinterIP(name, host string, port int) error {
	list, _ := a.spool.List()
	for i := range list {
		if list[i].Name == name {
			list[i].Port = "IP_" + host + "_" + strconv.Itoa(port)
		}
	}
	a.spool.Set(list)
	return nil
}
func (shopAdmin) Ports() ([]winps.PortDetail, []string, error) { return nil, nil, nil }
func (a shopAdmin) SetDriver(name, driver string) error {
	list, _ := a.spool.List()
	for i := range list {
		if list[i].Name == name {
			list[i].Driver = driver
		}
	}
	a.spool.Set(list)
	return nil
}

// Central management: commands from the server, the point watching its
// printers (Windows Update renames a printer, swaps a driver, removes it).
func TestManageAndWatch(t *testing.T) {
	agent.WatchEvery = 100 * time.Millisecond
	defer func() { agent.WatchEvery = 15 * time.Second }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sdir := t.TempDir()
	agentPort := freePort(t)
	common.SaveJSON(sdir+"/server.json", map[string]any{"listen": "127.0.0.1:" + strconv.Itoa(agentPort),
		"web": "127.0.0.1:" + strconv.Itoa(freePort(t)), "first_port": freePort(t), "wg_mode": testWGMode})
	srv, _ := server.New(sdir, common.NewLogRing("", 100), noAdmin{})
	srv.SetPublicAddr("127.0.0.1:" + strconv.Itoa(agentPort))
	go srv.Run(ctx)
	id, code, _ := srv.AddAgent("shop1")

	spool := &winprint.Fake{
		Printers: []winprint.PrinterInfo{{Name: "Xprinter XP-420B", Driver: "Xprinter XP-420B", Port: "USB002"},
			{Name: "Office", Driver: "HP", Port: "USB001"}},
		Versions: map[string]int{"Xprinter XP-420B": 3, "HP": 3, "Generic v4": 4},
	}
	adir := t.TempDir()
	common.SaveJSON(adir+"/agent.json", map[string]any{"web": "127.0.0.1:" + strconv.Itoa(freePort(t))})
	ag, _ := agent.New(adir, common.NewLogRing("", 100), spool)
	ag.Admin = shopAdmin{spool}
	ag.SetCode(code)
	go ag.Run(ctx)
	agentView := func() server.AgentView {
		for _, a := range srv.State().Agents {
			if a.ID == id {
				return a
			}
		}
		return server.AgentView{}
	}
	// OwnKey: the point reconnects once right after its first connection
	// (it swaps the code for its own key); commands go to the session after.
	waitFor(t, "точка на зв'язку з інвентарем", func() bool { a := agentView(); return a.Online && a.OwnKey && a.Inventory != nil })
	if n := len(agentView().Inventory.Printers); n != 2 {
		t.Fatalf("сервер бачить %d принтерів точки, мало бути 2", n)
	}

	// 1) Share a printer by a command from the server.
	args, _ := json.Marshal(map[string]any{"printers": []proto.Printer{{Name: "Xprinter XP-420B", Alias: "labels"}}})
	if _, err := srv.Command(id, "set_shared", args); err != nil {
		t.Fatal(err)
	}
	var port int
	waitFor(t, "сервер бачить виданий принтер", func() bool {
		for _, p := range agentView().Printers {
			if p.Alias == "labels" && p.Offered && p.ShopPort == "USB002" {
				port = p.Port
				return true
			}
		}
		return false
	})
	if _, err := srv.Command(id, "format_disk", json.RawMessage(`{}`)); err == nil {
		t.Fatal("невідома команда пройшла")
	}

	// 2) Windows Update renames the printer on the same port.
	spool.Set([]winprint.PrinterInfo{{Name: "Xprinter XP-420BM", Driver: "Xprinter XP-420B", Port: "USB002"},
		{Name: "Office", Driver: "HP", Port: "USB001"}})
	hasEvent := func(kind string) bool {
		for _, e := range agentView().Events {
			if e.Kind == kind {
				return true
			}
		}
		return false
	}
	waitFor(t, "подія «перейменовано»", func() bool { return hasEvent("renamed") })
	waitFor(t, "сервер знає нову назву", func() bool {
		for _, p := range agentView().Printers {
			if p.Alias == "labels" && p.Name == "Xprinter XP-420BM" && !p.ShopMissing {
				return true
			}
		}
		return false
	})
	if err := sendRaw(port, []byte("label after rename")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "друк після перейменування", func() bool { return len(spool.Jobs("Xprinter XP-420BM")) == 1 })

	// 3) Driver swapped to a v4 driver while in receipt/label mode -> warning.
	spool.Set([]winprint.PrinterInfo{{Name: "Xprinter XP-420BM", Driver: "Generic v4", Port: "USB002"},
		{Name: "Office", Driver: "HP", Port: "USB001"}})
	waitFor(t, "попередження про v4", func() bool {
		for _, e := range agentView().Events {
			if e.Kind == "driver_changed" && e.Level == "warn" && strings.Contains(e.Text, "v4") {
				return true
			}
		}
		return false
	})

	// 4) Printer gone (and nothing else on its port) -> warning: receipts keep
	// going through the service printer on the remembered port.
	spool.Set([]winprint.PrinterInfo{{Name: "Office", Driver: "HP", Port: "USB001"}})
	waitFor(t, "помилка «зник»", func() bool {
		for _, e := range agentView().Events {
			if e.Kind == "missing" && e.Level == "warn" {
				return true
			}
		}
		return false
	})
	if !agentView().Printers[0].ShopMissing {
		t.Fatal("панель не показує, що принтера немає")
	}

	if err := sendRaw(port, []byte("receipt while gone")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "чек через службову чергу, поки принтера немає", func() bool { return len(spool.Jobs("SlipDrukka (labels)")) == 1 })

	// 5) Back, but with an unmeasured v4 driver: still printed, through the
	// service printer; the shop's own printer is not touched.
	spool.Set([]winprint.PrinterInfo{{Name: "Xprinter XP-420BM", Driver: "Generic v4", Port: "USB002"},
		{Name: "Office", Driver: "HP", Port: "USB001"}})
	waitFor(t, "принтер знову на місці", func() bool { return !agentView().Printers[0].ShopMissing })
	if err := sendRaw(port, []byte("receipt on v4")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "чек на v4 через службову чергу", func() bool { return len(spool.Jobs("SlipDrukka (labels)")) == 2 })
	if got := string(spool.Jobs("SlipDrukka (labels)")[1]); got != "receipt on v4" {
		t.Fatalf("надруковано не те: %q", got)
	}
	waitFor(t, "подія про службову чергу", func() bool { return hasEvent("fallback") })

	// 6) Someone fixes the driver -> the service printer goes away by itself
	// and receipts go to the shop printer again.
	args, _ = json.Marshal(map[string]string{"name": "Xprinter XP-420BM", "driver": ""})
	if _, err := srv.Command(id, "set_driver", args); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "службову чергу прибрано", func() bool {
		list, _ := spool.List()
		for _, p := range list {
			if p.Name == "SlipDrukka (labels)" {
				return false
			}
		}
		return true
	})
	if err := sendRaw(port, []byte("receipt after fix")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "чек напряму в принтер магазину", func() bool { return len(spool.Jobs("Xprinter XP-420BM")) == 2 })

	// 6b) IP change from the panel: same printer name, same server port.
	args, _ = json.Marshal(map[string]any{"name": "Xprinter XP-420BM", "host": "10.0.0.9", "port": 9100})
	if _, err := srv.Command(id, "set_printer_ip", args); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "сервер бачить нову адресу", func() bool {
		p := agentView().Printers[0]
		return p.ShopPort == "IP_10.0.0.9_9100" && p.Port == port
	})

	// 7) Free the port: refused while the printer is offered, works after.
	if err := srv.FreePort(id, "labels"); err == nil {
		t.Fatal("порт виданого принтера звільнено")
	}
	args, _ = json.Marshal(map[string]any{"printers": []proto.Printer{}})
	if _, err := srv.Command(id, "set_shared", args); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "принтер прибрано", func() bool { return !agentView().Printers[0].Offered })
	if err := srv.FreePort(id, "labels"); err != nil {
		t.Fatal(err)
	}
	if n := len(agentView().Printers); n != 0 {
		t.Fatalf("після звільнення порту лишилось %d рядків", n)
	}
}
