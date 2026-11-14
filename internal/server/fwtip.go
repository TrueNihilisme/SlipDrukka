package server

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// The service has no administrator rights and cannot change firewall rules
// (measured: "The requested operation requires elevation"). Instead of widening
// its rights, the Settings page shows the administrator one PowerShell command
// that narrows the program's rules to the points port and the shops' addresses.

// FWRule is one Windows rule as read by FirewallRead.
type FWRule struct {
	Name     string   `json:"name"`
	Protocol string   `json:"protocol"`
	Ports    []string `json:"ports"`
	Remote   []string `json:"remote"`
}

// FirewallTip is GET /api/firewall.
type FirewallTip struct {
	Available  bool     `json:"available"` // rules could be read (Windows)
	Error      string   `json:"error,omitempty"`
	Rules      []FWRule `json:"rules"`
	Restricted bool     `json:"restricted"` // every rule limited by port and address
	Missing    []string `json:"missing"`    // shop addresses the rules do not admit
	Port       int      `json:"port"`
	Addresses  []string `json:"addresses"`
	Command    string   `json:"command"`
	Revert     string   `json:"revert"`
	WebLAN     bool     `json:"web_lan"`
	WebPort    int      `json:"web_port"`
}

func (s *Server) shopAddresses() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[string]bool{}
	add := func(list []string) {
		for _, e := range list {
			e = strings.TrimSpace(e)
			if ip := net.ParseIP(e); ip != nil && ip.IsLoopback() {
				continue
			}
			if e != "" {
				set[e] = true
			}
		}
	}
	add(s.cfg.Allowed)
	for _, a := range s.cfg.Agents {
		add(a.AllowedIPs)
		add(a.Seen)
	}
	out := make([]string, 0, len(set))
	for e := range set {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func fwCommand(port int, remote []string) string {
	q := make([]string, len(remote))
	for i, r := range remote {
		q[i] = psQuote(r)
	}
	portArg, remoteArg := fmt.Sprint(port), "$ips"
	head := "$ips = @(" + strings.Join(q, ",") + ")\n"
	if len(remote) == 0 {
		portArg, remoteArg, head = "Any", "Any", ""
	}
	return head + "Get-NetFirewallRule -DisplayName " + psQuote(fwAgents) + "," + psQuote(fwAgentsWG) +
		" -ErrorAction SilentlyContinue | ForEach-Object { $_ | Get-NetFirewallPortFilter | Set-NetFirewallPortFilter -LocalPort " +
		portArg + "; $_ | Set-NetFirewallRule -RemoteAddress " + remoteArg + " }"
}

// Firewall builds the Settings tip. Reading the rules runs PowerShell, so the
// result is kept for a few seconds.
func (s *Server) Firewall() FirewallTip {
	p := s.ports()
	t := FirewallTip{Port: p.AgentPort, WebLAN: p.WebLAN, WebPort: p.WebPort, Addresses: s.shopAddresses(), Rules: []FWRule{}, Missing: []string{}}
	t.Revert = fwCommand(p.AgentPort, nil)
	if len(t.Addresses) > 0 {
		t.Command = fwCommand(p.AgentPort, t.Addresses)
	}
	if s.FirewallRead == nil {
		return t
	}
	s.fwMu.Lock()
	if time.Since(s.fwAt) > 5*time.Second {
		s.fwRules, s.fwErr = s.FirewallRead(fwAgents, fwAgentsWG)
		s.fwAt = time.Now()
	}
	rules, err := s.fwRules, s.fwErr
	s.fwMu.Unlock()
	if err != nil {
		t.Error = err.Error()
		return t
	}
	t.Available = true
	t.Rules = append(t.Rules, rules...)
	t.Restricted = len(rules) > 0
	admitted := map[string]bool{}
	for _, r := range rules {
		if len(r.Ports) == 0 || hasAny(r.Ports) || len(r.Remote) == 0 || hasAny(r.Remote) {
			t.Restricted = false
		}
		for _, a := range r.Remote {
			admitted[a] = true
		}
	}
	if t.Restricted {
		for _, a := range t.Addresses {
			// Windows shows a single address as a /32 (or /128) network.
			if !admitted[a] && !admitted[a+"/32"] && !admitted[a+"/128"] && !admitted[a+"/255.255.255.255"] {
				t.Missing = append(t.Missing, a)
			}
		}
	}
	return t
}

func hasAny(list []string) bool {
	for _, e := range list {
		if strings.EqualFold(e, "Any") {
			return true
		}
	}
	return false
}
