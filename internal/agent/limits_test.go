package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/winprint"
)

// A shop printer is on the shop's network. Addresses elsewhere are refused,
// so a server in the wrong hands cannot send the shop's printing to itself.
func TestPrinterHostMustBeLocal(t *testing.T) {
	for _, ok := range []string{"192.168.1.50", "10.0.0.9", "172.16.4.2", "172.31.255.1", "169.254.10.10", "127.0.0.1",
		"fd00::5", "fe80::1", "::ffff:192.168.1.50", "printer", "XP-420B", "kasa1.local", "label.shop.lan", "p1.home.arpa"} {
		if err := checkPrinterHost(ok); err != nil {
			t.Errorf("локальну адресу %q відхилено: %v", ok, err)
		}
	}
	for _, bad := range []string{"8.8.8.8", "203.0.113.10", "172.32.0.1", "192.169.1.1", "100.64.0.1", "2001:db8::1", "::ffff:8.8.8.8",
		"example.com", "printer.example.org", "192.168.1", "134744072", "0x08080808", "1printer", "a b", "x;y", "", "-p", ".local"} {
		if err := checkPrinterHost(bad); err == nil {
			t.Errorf("нелокальну або некоректну адресу %q прийнято", bad)
		}
	}
}

// The server may rename or re-point only printers that the point offers to
// it or that this program created; the shop's other printers are off limits.
func TestCommandsOnlyOnOurPrinters(t *testing.T) {
	spool := &winprint.Fake{Printers: []winprint.PrinterInfo{
		{Name: "Каса", Driver: "Generic / Text Only", Port: "USB001"},
		{Name: "Бухгалтерія", Driver: "HP", Port: "IP_192.168.1.20"},
	}}
	a, _ := New(t.TempDir(), common.NewLogRing("", 20), spool)
	fa := &fakeAdmin{spool: spool}
	a.Admin = fa
	if err := a.SetShared([]proto.Printer{{Name: "Каса", Alias: "kasa"}}); err != nil {
		t.Fatal(err)
	}
	cmd := func(name string, args any) error {
		b, _ := json.Marshal(args)
		_, err := a.runCmd(name, b)
		return err
	}

	// The office printer is neither offered nor ours.
	for name, args := range map[string]any{
		"rename_printer": map[string]string{"old": "Бухгалтерія", "new": "X"},
		"set_driver":     map[string]string{"name": "Бухгалтерія", "driver": ""},
		"set_printer_ip": map[string]any{"name": "Бухгалтерія", "host": "192.168.1.99", "port": 9100},
	} {
		if err := cmd(name, args); err == nil || !strings.Contains(err.Error(), "не видано") {
			t.Fatalf("%s на чужому принтері: %v", name, err)
		}
	}
	// An offered printer: allowed, but never to an address outside the shop.
	if err := cmd("set_printer_ip", map[string]any{"name": "Каса", "host": "203.0.113.7", "port": 9100}); err == nil {
		t.Fatal("принтер перенаправлено на адресу в інтернеті")
	}
	if err := cmd("set_printer_ip", map[string]any{"name": "Каса", "host": "192.168.1.70", "port": 9100}); err != nil {
		t.Fatal(err)
	}
	if err := cmd("add_net_printer", map[string]any{"name": "Зовні", "host": "198.51.100.4"}); err == nil {
		t.Fatal("створено принтер з адресою в інтернеті")
	}
	// A printer this program created is ours even when it is not offered,
	// and stays ours after a rename.
	if err := cmd("add_net_printer", map[string]any{"name": "Етикетки", "host": "192.168.1.60"}); err != nil {
		t.Fatal(err)
	}
	if err := cmd("rename_printer", map[string]string{"old": "Етикетки", "new": "Етикетки 2"}); err != nil {
		t.Fatal(err)
	}
	if !a.ours("Етикетки 2") || a.ours("Етикетки") {
		t.Fatalf("створені принтери: %v", a.cfg.Created)
	}
	// The command that could open the point's page to the network is gone.
	if err := cmd("set_panel", map[string]any{"web_lan": true, "web_port": 7320}); err == nil || !strings.Contains(err.Error(), "невідома команда") {
		t.Fatalf("set_panel: %v", err)
	}
}

// The point's page has no password: it is served to this computer only,
// even if an old settings file asks for all interfaces.
func TestPanelAlwaysLoopback(t *testing.T) {
	dir := t.TempDir()
	common.SaveJSON(dir+"/agent.json", map[string]any{"web": "0.0.0.0:7777"})
	a, err := New(dir, common.NewLogRing("", 5), &winprint.Fake{})
	if err != nil {
		t.Fatal(err)
	}
	if a.WebAddr() != "127.0.0.1:7777" {
		t.Fatalf("сторінка точки слухає %s", a.WebAddr())
	}
}
