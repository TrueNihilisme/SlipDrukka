package server

import (
	"errors"
	"strings"
	"testing"
)

func TestFirewallCommand(t *testing.T) {
	c := fwCommand(7310, []string{"203.0.113.9", "198.51.100.0/24", "a'b"})
	for _, want := range []string{"$ips = @('203.0.113.9','198.51.100.0/24','a''b')", "-DisplayName 'SlipDrukka Server','SlipDrukka Server WireGuard'", "Set-NetFirewallPortFilter -LocalPort 7310", "Set-NetFirewallRule -RemoteAddress $ips"} {
		if !strings.Contains(c, want) {
			t.Errorf("command lacks %q:\n%s", want, c)
		}
	}
	r := fwCommand(7310, nil)
	if !strings.Contains(r, "-LocalPort Any") || !strings.Contains(r, "-RemoteAddress Any") || strings.Contains(r, "$ips =") {
		t.Errorf("revert command wrong:\n%s", r)
	}
	for _, b := range []byte(c + r) {
		if b > 127 {
			t.Fatalf("command must be ASCII (Windows PowerShell 5.1): %q", c+r)
		}
	}
}

func TestFirewallTipState(t *testing.T) {
	s, _ := newGuardServer(t)
	s.cfg.Allowed = []string{"203.0.113.9"}
	tip := s.Firewall()
	if tip.Available || tip.Command == "" || len(tip.Addresses) != 1 {
		t.Fatalf("no reader: %+v", tip)
	}
	read := func(rules []FWRule, err error) {
		s.FirewallRead = func(...string) ([]FWRule, error) { return rules, err }
		s.fwAt = s.fwAt.AddDate(-1, 0, 0)
	}
	read([]FWRule{{Name: fwAgents, Protocol: "TCP", Ports: []string{"Any"}, Remote: []string{"Any"}}}, nil)
	if tip = s.Firewall(); !tip.Available || tip.Restricted {
		t.Fatalf("open rule: %+v", tip)
	}
	read([]FWRule{{Name: fwAgents, Protocol: "TCP", Ports: []string{"7310"}, Remote: []string{"203.0.113.9/32"}}}, nil)
	if tip = s.Firewall(); !tip.Restricted || len(tip.Missing) != 0 {
		t.Fatalf("restricted rule: %+v", tip)
	}
	s.cfg.Allowed = append(s.cfg.Allowed, "198.51.100.7")
	read([]FWRule{{Name: fwAgents, Protocol: "TCP", Ports: []string{"7310"}, Remote: []string{"203.0.113.9/32"}}}, nil)
	if tip = s.Firewall(); len(tip.Missing) != 1 || tip.Missing[0] != "198.51.100.7" {
		t.Fatalf("missing address: %+v", tip)
	}
	read(nil, errors.New("boom"))
	if tip = s.Firewall(); tip.Available || tip.Error == "" {
		t.Fatalf("read error: %+v", tip)
	}
}
