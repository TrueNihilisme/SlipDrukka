package server

// Source address allow-list for the port points connect to.
//
// Two layers: the Windows firewall rule only admits the listed sources, and
// the server itself drops anything else right after accept (so it still
// holds if the firewall is off or the rule was edited). A point can also be
// tied to its own addresses: its code is then refused from anywhere else.
// Refused sources are counted and shown in the admin page with an
// "allow" button, so a shop whose ISP changed its IP is easy to fix.

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"
)

type refusedInfo struct {
	Count  int
	Last   time.Time
	Reason string
}

type RefusedView struct {
	IP     string    `json:"ip"`
	Count  int       `json:"count"`
	Last   time.Time `json:"last"`
	Reason string    `json:"reason"`
}

// ParseAllowList validates entries (IPv4/IPv6 address or CIDR), drops
// duplicates and returns them in canonical form. Separators: comma, space,
// semicolon or new line.
func ParseAllowList(text string) ([]string, error) {
	fields := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' })
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		p, err := parseEntry(f)
		if err != nil {
			return nil, fmt.Errorf("«%s» — не IP-адреса і не мережа (приклад: 203.0.113.10 або 198.51.100.0/24)", f)
		}
		if p.Bits() == 0 {
			return nil, fmt.Errorf("«%s» дозволяє весь інтернет — залиште поле порожнім, якщо це потрібно", f)
		}
		s := p.String()
		if p.IsSingleIP() {
			s = p.Addr().String()
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) > 500 {
		return nil, errors.New("забагато адрес (понад 500)")
	}
	return out, nil
}

func parseEntry(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return p, err
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func matches(list []string, ip netip.Addr) bool {
	for _, e := range list {
		if p, err := parseEntry(e); err == nil && p.Contains(ip) {
			return true
		}
	}
	return false
}

func remoteIP(c net.Conn) netip.Addr {
	if ta, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		if a, ok := netip.AddrFromSlice(ta.IP); ok {
			return a.Unmap()
		}
	}
	ap, err := netip.ParseAddrPort(c.RemoteAddr().String())
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

// sourceAllowed: the global list, plus any point-specific address (so a
// point's own IP works even if the admin forgot to add it globally).
func (s *Server) sourceAllowed(ip netip.Addr) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cfg.Allowed) == 0 {
		return true
	}
	if matches(s.cfg.Allowed, ip) {
		return true
	}
	for _, a := range s.cfg.Agents {
		if matches(a.AllowedIPs, ip) {
			return true
		}
	}
	return false
}

func (s *Server) agentSourceAllowed(a *AgentConfig, ip netip.Addr) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(a.AllowedIPs) == 0 || matches(a.AllowedIPs, ip)
}

func (s *Server) noteRefused(ip netip.Addr, reason string) {
	key := ip.String()
	s.mu.Lock()
	r := s.refused[key]
	if r == nil {
		if len(s.refused) > 200 { // keep memory bounded
			s.refused = map[string]*refusedInfo{}
		}
		r = &refusedInfo{}
		s.refused[key] = r
	}
	r.Count++
	r.Last = time.Now()
	r.Reason = reason
	first := r.Count == 1
	s.mu.Unlock()
	if first {
		s.Log.Printf("відхилено з'єднання з %s: %s", key, reason)
	}
}

func (s *Server) refusedViewsLocked() []RefusedView {
	var out []RefusedView
	for ip, r := range s.refused {
		out = append(out, RefusedView{IP: ip, Count: r.Count, Last: r.Last, Reason: r.Reason})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

// firewallRemotes is what the Windows rule should admit: nil (= any) when
// the global list is empty, otherwise the global list plus point addresses.
func (s *Server) firewallRemotesLocked() []string {
	if len(s.cfg.Allowed) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(list []string) {
		for _, e := range list {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	add(s.cfg.Allowed)
	for _, a := range s.cfg.Agents {
		add(a.AllowedIPs)
	}
	return out
}

// applyFirewall (re)writes the Windows rule for the points port.
func (s *Server) applyFirewall() {
	if s.FirewallAdd == nil {
		return
	}
	p := s.ports()
	s.mu.Lock()
	remotes := s.firewallRemotesLocked()
	mode := s.cfg.WGMode
	s.mu.Unlock()
	remove := func(name string) {
		if s.FirewallRemove != nil {
			s.FirewallRemove(name)
		}
	}
	if mode == WGOff {
		remove(fwAgentsWG)
	} else if err := s.FirewallAdd(fwAgentsWG, "UDP", p.AgentPort, remotes); err != nil {
		s.Log.Printf("ПОМИЛКА брандмауера: %v", err)
	}
	if mode == WGOnly {
		remove(fwAgents)
		return
	}
	if err := s.FirewallAdd(fwAgents, "TCP", p.AgentPort, remotes); err != nil {
		s.Log.Printf("ПОМИЛКА брандмауера: %v", err)
		return
	}
	if remotes == nil {
		s.Log.Printf("брандмауер: порт %d відкрито для будь-яких адрес", p.AgentPort)
	} else {
		s.Log.Printf("брандмауер: порт %d лише для %s", p.AgentPort, strings.Join(remotes, ", "))
	}
}

// WouldDropError lists connected points a new list would cut off. The
// admin page shows it and asks for confirmation (force).
type WouldDropError struct{ Points []string }

func (e *WouldDropError) Error() string {
	return "ці точки зараз підключені й будуть відключені: " + strings.Join(e.Points, "; ")
}

// OnlineSources lists "name — ip" of connected points (for the page).
func (s *Server) OnlineSources() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for id, c := range s.online {
		name := s.agentNameLocked(id)
		if ap, err := netip.ParseAddrPort(c.remote); err == nil {
			out = append(out, name+" — "+ap.Addr().Unmap().String())
		}
	}
	sort.Strings(out)
	return out
}

// SetAllowed replaces the global list. Without force it refuses a list that
// would disconnect points that are online right now.
func (s *Server) SetAllowed(text string, force bool) error {
	list, err := ParseAllowList(text)
	if err != nil {
		return err
	}
	if !force && len(list) > 0 {
		var cut []string
		s.mu.Lock()
		for id, c := range s.online {
			ap, err := netip.ParseAddrPort(c.remote)
			if err != nil {
				continue
			}
			ip := ap.Addr().Unmap()
			ok := matches(list, ip)
			for _, a := range s.cfg.Agents {
				if matches(a.AllowedIPs, ip) {
					ok = true
				}
			}
			if !ok {
				cut = append(cut, s.agentNameLocked(id)+" ("+ip.String()+")")
			}
		}
		s.mu.Unlock()
		if len(cut) > 0 {
			sort.Strings(cut)
			return &WouldDropError{Points: cut}
		}
	}
	s.mu.Lock()
	s.cfg.Allowed = list
	s.unbanListedLocked()
	err = s.save()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.applyFirewall()
	s.dropDisallowed()
	return nil
}

// SetAgentAllowed ties one point to its addresses ("" removes the tie).
func (s *Server) SetAgentAllowed(id, text string) error {
	list, err := ParseAllowList(text)
	if err != nil {
		return err
	}
	s.mu.Lock()
	a := s.findAgentLocked(id)
	if a == nil {
		s.mu.Unlock()
		return errors.New("точки немає")
	}
	a.AllowedIPs = list
	s.unbanListedLocked()
	err = s.save()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.applyFirewall()
	s.dropDisallowed()
	return nil
}

// dropDisallowed closes sessions whose source is no longer allowed.
func (s *Server) dropDisallowed() {
	s.mu.Lock()
	var drop []*agentConn
	for id, c := range s.online {
		ip, err := netip.ParseAddrPort(c.remote)
		if err != nil {
			continue
		}
		addr := ip.Addr().Unmap()
		a := s.findAgentLocked(id)
		globalOK := len(s.cfg.Allowed) == 0 || matches(s.cfg.Allowed, addr)
		if !globalOK {
			for _, x := range s.cfg.Agents {
				if matches(x.AllowedIPs, addr) {
					globalOK = true
				}
			}
		}
		if !globalOK || (a != nil && len(a.AllowedIPs) > 0 && !matches(a.AllowedIPs, addr)) {
			drop = append(drop, c)
		}
	}
	s.mu.Unlock()
	for _, c := range drop {
		s.Log.Printf("точку з %s відключено: адреса більше не дозволена", c.remote)
		c.sess.Close()
	}
}

// unbanListedLocked lifts bans of addresses the admin has just allowed.
func (s *Server) unbanListedLocked() {
	for key := range s.cfg.Bans {
		ip, err := netip.ParseAddr(key)
		if err != nil {
			continue
		}
		listed := len(s.cfg.Allowed) > 0 && matches(s.cfg.Allowed, ip)
		for _, a := range s.cfg.Agents {
			if matches(a.AllowedIPs, ip) {
				listed = true
			}
		}
		if listed {
			delete(s.cfg.Bans, key)
			s.Log.Printf("адресу %s розблоковано: її додано до дозволених", key)
		}
	}
}
