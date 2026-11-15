package server

// Protection of the port points connect to, on top of the allow-list
// (allow.go):
//
//   - an address with failMax failed connections within failWindow is
//     blocked for BanHours (built-in "fail2ban"). Failed: a broken or garbage
//     handshake, an unknown or expired code. Not counted: an old program
//     version, addresses on an allow-list, addresses a point connected from
//     in the last knownFor (a shop whose point still has an old code);
//   - a blocked address is served only if it shows a valid code, which also
//     lifts the block; anything else is closed silently;
//   - connections that have not shown a code yet are limited per address and
//     in total; part of the total is kept for addresses of known shops, and
//     connections through WireGuard have a total of their own;
//   - failures, bans and these limits count an IPv6 /64 network as one
//     address (guardKey);
//   - a point seen from a new address raises an alert (or, with AutoPin, a
//     point without addresses is tied to the first one);
//   - a code nobody used expires after CodeTTLDays.

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"time"

	"slipdrukka/internal/common"
)

const (
	failMax       = 5
	failWindow    = 10 * time.Minute
	knownFor      = 30 * 24 * time.Hour
	maxPendingIP  = 4
	maxPendingAll = 64
	// maxPendingNew: how many of the maxPendingAll slots addresses nobody
	// knows may hold; the rest is kept for allowed addresses and addresses
	// shops connected from, so strangers cannot keep shops from reconnecting.
	maxPendingNew = 48
	// maxPendingWG: slots for connections through WireGuard. Their own total:
	// only a shop's key gets that far, and a full TCP total must not stop it.
	maxPendingWG = 64
	// knownRefresh: how often the "last seen" time of a known address is
	// written to the config (it only feeds the knownFor limit).
	knownRefresh   = time.Hour
	maxBans        = 5000
	maxAlerts      = 50
	maxSeen        = 10
	defaultBanH    = 120 // 5 days
	defaultCodeD   = 7
	foreverSetting = -1
)

// helloWait: how long an address nobody knows has to send its greeting;
// known addresses, shops' WireGuard keys and everything after the greeting
// get helloWaitKnown. A shop builds its greeting before it dials, so it has
// nothing to wait for. (Variables: the tests shorten them.)
var (
	helloWait      = 5 * time.Second
	helloWaitKnown = 20 * time.Second
)

// BanChoices and CodeChoices are what the settings page offers.
var (
	BanChoices  = []int{1, 24, 120, 720, foreverSetting}
	CodeChoices = []int{1, 7, 30, foreverSetting}
)

type Ban struct {
	Since  time.Time `json:"since"`
	Until  time.Time `json:"until,omitempty"` // zero: until removed by hand
	Reason string    `json:"reason"`
	Hits   int       `json:"hits"` // connections dropped since
}

type Alert struct {
	ID    string    `json:"id"`
	Agent string    `json:"agent"`
	Name  string    `json:"name"`
	IP    string    `json:"ip"`
	Prev  []string  `json:"prev"`
	At    time.Time `json:"at"`
}

type BanView struct {
	IP      string    `json:"ip"`
	Since   time.Time `json:"since"`
	Until   time.Time `json:"until"`
	Forever bool      `json:"forever"`
	Reason  string    `json:"reason"`
	Hits    int       `json:"hits"`
}

type SecurityView struct {
	WGMode      string   `json:"wg_mode"`
	WGPort      int      `json:"wg_port"`
	WGRunning   bool     `json:"wg_running"`
	WGMissing   []string `json:"wg_missing"` // points whose WireGuard key the server does not know yet
	BanHours    int      `json:"ban_hours"`
	CodeTTLDays int      `json:"code_ttl_days"`
	AutoPin     bool     `json:"auto_pin"`
	BanChoices  []int    `json:"ban_choices"`
	CodeChoices []int    `json:"code_choices"`
}

func (s *Server) banHoursLocked() int {
	if s.cfg.BanHours == 0 {
		return defaultBanH
	}
	return s.cfg.BanHours
}

func (s *Server) codeDaysLocked() int {
	if s.cfg.CodeTTLDays == 0 {
		return defaultCodeD
	}
	return s.cfg.CodeTTLDays
}

func (s *Server) securityLocked() SecurityView {
	v := SecurityView{BanHours: s.banHoursLocked(), CodeTTLDays: s.codeDaysLocked(), AutoPin: s.cfg.AutoPin,
		BanChoices: BanChoices, CodeChoices: CodeChoices, WGMode: s.cfg.WGMode, WGRunning: s.wg != nil}
	if s.wg != nil {
		v.WGPort = s.wg.Port()
	}
	for _, a := range s.cfg.Agents {
		if a.WGPub == "" {
			v.WGMissing = append(v.WGMissing, a.Name)
		}
	}
	return v
}

// codeExpiresLocked: when the point's unused code stops working (zero: never).
func (s *Server) codeExpiresLocked(a *AgentConfig) time.Time {
	d := s.codeDaysLocked()
	if a.CodeUsed || a.CodeIssued.IsZero() || d == foreverSetting {
		return time.Time{}
	}
	return a.CodeIssued.Add(time.Duration(d) * 24 * time.Hour)
}

func (s *Server) codeExpiredLocked(a *AgentConfig, now time.Time) bool {
	exp := s.codeExpiresLocked(a)
	return !exp.IsZero() && now.After(exp)
}

// guardKey is what failures, bans, known addresses and handshake slots are
// counted by: the address itself for IPv4, the /64 network for IPv6. One
// subscriber owns a whole /64, so counting single addresses there would cost
// an attacker nothing.
func guardKey(ip netip.Addr) string {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() || ip.Is4() {
		return ip.String()
	}
	p, err := ip.Prefix(64)
	if err != nil {
		return ip.String()
	}
	return p.String()
}

// keyAddr turns a guardKey (or an address written by an older version) back
// into an address inside it.
func keyAddr(key string) (netip.Addr, bool) {
	if p, err := netip.ParsePrefix(key); err == nil {
		return p.Addr(), true
	}
	if a, err := netip.ParseAddr(key); err == nil {
		return a.Unmap(), true
	}
	return netip.Addr{}, false
}

// rekeyGuardLocked moves bans and known addresses written by versions that
// counted single IPv6 addresses to their /64 network.
func (s *Server) rekeyGuardLocked() {
	for k, b := range s.cfg.Bans {
		if a, err := netip.ParseAddr(k); err == nil {
			if nk := guardKey(a); nk != k {
				if s.cfg.Bans[nk] == nil {
					s.cfg.Bans[nk] = b
				}
				delete(s.cfg.Bans, k)
			}
		}
	}
	for k, t := range s.cfg.KnownIPs {
		if a, err := netip.ParseAddr(k); err == nil {
			if nk := guardKey(a); nk != k {
				if t.After(s.cfg.KnownIPs[nk]) {
					s.cfg.KnownIPs[nk] = t
				}
				delete(s.cfg.KnownIPs, k)
			}
		}
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// exemptLocked: addresses that are never counted or banned.
func (s *Server) exemptLocked(ip netip.Addr, now time.Time) bool {
	if len(s.cfg.Allowed) > 0 && matches(s.cfg.Allowed, ip) {
		return true
	}
	for _, a := range s.cfg.Agents {
		if matches(a.AllowedIPs, ip) {
			return true
		}
	}
	if t, ok := s.cfg.KnownIPs[guardKey(ip)]; ok && now.Sub(t) < knownFor {
		return true
	}
	return false
}

// banLocked returns the address's active ban; an expired one is removed.
func (s *Server) banLocked(ip string, now time.Time) *Ban {
	b := s.cfg.Bans[ip]
	if b == nil {
		return nil
	}
	if !b.Until.IsZero() && now.After(b.Until) {
		delete(s.cfg.Bans, ip)
		return nil
	}
	return b
}

func (s *Server) isBanned(ip netip.Addr) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.exemptLocked(ip, now) {
		return false
	}
	return s.banLocked(guardKey(ip), now) != nil || s.csBanned(ip)
}

// exempt: see exemptLocked.
func (s *Server) exempt(ip netip.Addr) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exemptLocked(ip, time.Now())
}

func (s *Server) noteBannedHit(ip netip.Addr) {
	s.mu.Lock()
	if b := s.cfg.Bans[guardKey(ip)]; b != nil {
		b.Hits++
	}
	s.mu.Unlock()
}

// noteFail counts a failed connection and bans the address on failMax.
// code is the reason in the stable form written for CrowdSec.
func (s *Server) noteFail(ip netip.Addr, reason string, code csFailCode) {
	if !ip.IsValid() {
		return
	}
	key := guardKey(ip)
	now := time.Now()
	s.mu.Lock()
	if s.exemptLocked(ip, now) || s.banLocked(key, now) != nil {
		s.mu.Unlock()
		return
	}
	go s.csWriteLog(ip, code) // never for exempt addresses: CrowdSec must not block a shop
	if len(s.fails) > 10000 { // keep memory bounded
		for k, list := range s.fails {
			if len(list) == 0 || now.Sub(list[len(list)-1]) > failWindow {
				delete(s.fails, k)
			}
		}
	}
	keep := s.fails[key][:0]
	for _, t := range s.fails[key] {
		if now.Sub(t) < failWindow {
			keep = append(keep, t)
		}
	}
	keep = append(keep, now)
	if len(keep) < failMax {
		s.fails[key] = keep
		s.mu.Unlock()
		return
	}
	delete(s.fails, key)
	if len(s.cfg.Bans) >= maxBans {
		s.mu.Unlock()
		s.Log.Printf("ПОМИЛКА: список заблокованих адрес повний (%d), %s не заблоковано", maxBans, key)
		return
	}
	b := &Ban{Since: now, Reason: reason}
	h := s.banHoursLocked()
	if h != foreverSetting {
		b.Until = now.Add(time.Duration(h) * time.Hour)
	}
	s.cfg.Bans[key] = b
	err := s.save()
	s.mu.Unlock()
	if b.Until.IsZero() {
		s.Log.Printf("адресу %s заблоковано назавжди: %d невдалих підключень за %d хв (%s)", key, failMax, int(failWindow/time.Minute), reason)
	} else {
		s.Log.Printf("адресу %s заблоковано до %s: %d невдалих підключень за %d хв (%s)", key, b.Until.Format("02.01 15:04"), failMax, int(failWindow/time.Minute), reason)
	}
	if err != nil {
		s.Log.Printf("ПОМИЛКА збереження конфігу: %v", err)
	}
}

// noteGood records a successful connection of point id from ip: the address
// becomes known (and unbanned), the code counts as used, a new address
// raises an alert or, with AutoPin, ties the code to the first address.
func (s *Server) noteGood(id string, ip netip.Addr) {
	key := ip.String() // the point's address, as shown to the admin
	gk := guardKey(ip) // what bans and known addresses are counted by
	now := time.Now()
	var logs []string
	pinned := false
	s.mu.Lock()
	a := s.findAgentLocked(id)
	if a == nil {
		s.mu.Unlock()
		return
	}
	// The config is written (and synced to the disk, under s.mu) only when
	// something in it changed: after a server restart every shop reconnects
	// at once, and a write per shop would hold up everything else.
	changed := false
	for k, t := range s.cfg.KnownIPs {
		if now.Sub(t) > knownFor {
			delete(s.cfg.KnownIPs, k)
			changed = true
		}
	}
	if t, ok := s.cfg.KnownIPs[gk]; !ok || now.Sub(t) > knownRefresh {
		s.cfg.KnownIPs[gk] = now
		changed = true
	}
	delete(s.fails, gk)
	if s.cfg.Bans[gk] != nil {
		delete(s.cfg.Bans, gk)
		changed = true
		logs = append(logs, fmt.Sprintf("адресу %s розблоковано: точка «%s» підключилась з дійсним кодом", gk, a.Name))
	}
	if !a.CodeUsed {
		a.CodeUsed = true
		changed = true
	}
	if !contains(a.Seen, key) {
		switch {
		case len(a.Seen) == 0 && s.cfg.AutoPin && len(a.AllowedIPs) == 0:
			a.AllowedIPs = []string{key}
			pinned = true
			logs = append(logs, fmt.Sprintf("точка «%s»: код прив'язано до першої адреси %s", a.Name, key))
		case len(a.Seen) > 0 && !matches(a.AllowedIPs, ip):
			al := &Alert{ID: common.RandomToken(6), Agent: a.ID, Name: a.Name, IP: key, Prev: append([]string(nil), a.Seen...), At: now}
			s.cfg.Alerts = append(s.cfg.Alerts, al)
			if len(s.cfg.Alerts) > maxAlerts {
				s.cfg.Alerts = s.cfg.Alerts[len(s.cfg.Alerts)-maxAlerts:]
			}
			logs = append(logs, fmt.Sprintf("УВАГА: точка «%s» підключилась з нової адреси %s (раніше: %s)", a.Name, key, joinShort(a.Seen)))
		}
		a.Seen = append(a.Seen, key)
		if len(a.Seen) > maxSeen {
			a.Seen = a.Seen[len(a.Seen)-maxSeen:]
		}
		changed = true
	} else if a.Seen[len(a.Seen)-1] != key { // keep the latest address last
		out := a.Seen[:0]
		for _, x := range a.Seen {
			if x != key {
				out = append(out, x)
			}
		}
		a.Seen = append(out, key)
		changed = true
	}
	var err error
	if changed {
		err = s.save()
	}
	s.mu.Unlock()
	for _, l := range logs {
		s.Log.Printf("%s", l)
	}
	if err != nil {
		s.Log.Printf("ПОМИЛКА збереження конфігу: %v", err)
	}
	if pinned {
		s.applyFirewall()
	}
}

func joinShort(list []string) string {
	out := ""
	for i, x := range list {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

// enterHandshake takes a slot for a connection that has not shown a code
// yet; ok=false means the limit is reached and the connection is dropped.
// Addresses nobody knows share maxPendingNew of the maxPendingAll slots.
func (s *Server) enterHandshake(ip netip.Addr, banned bool) (release func(), ok bool) {
	per := maxPendingIP
	if banned {
		per = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := maxPendingNew
	if !banned && s.exemptLocked(ip, time.Now()) {
		limit = maxPendingAll
	}
	return s.takeSlotLocked(s.pending, &s.pendAll, guardKey(ip), per, limit)
}

// enterHandshakeWG is enterHandshake for a connection through WireGuard.
func (s *Server) enterHandshakeWG(ip netip.Addr) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takeSlotLocked(s.pendingWG, &s.pendWG, guardKey(ip), maxPendingIP, maxPendingWG)
}

func (s *Server) takeSlotLocked(by map[string]int, all *int, key string, per, limit int) (release func(), ok bool) {
	if *all >= limit || by[key] >= per {
		return nil, false
	}
	*all++
	by[key]++
	return func() {
		s.mu.Lock()
		*all--
		if by[key]--; by[key] <= 0 {
			delete(by, key)
		}
		s.mu.Unlock()
	}, true
}

func (s *Server) bansLocked(now time.Time) []BanView {
	var out []BanView
	for ip, b := range s.cfg.Bans {
		if !b.Until.IsZero() && now.After(b.Until) {
			continue
		}
		out = append(out, BanView{IP: ip, Since: b.Since, Until: b.Until, Forever: b.Until.IsZero(), Reason: b.Reason, Hits: b.Hits})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.After(out[j].Since) })
	return out
}

// Unban lifts the block of one address (or IPv6 /64 network).
func (s *Server) Unban(ip string) error {
	a, ok := keyAddr(ip)
	if !ok {
		return errors.New("некоректна адреса")
	}
	key := guardKey(a)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Bans[key] == nil {
		return errors.New("ця адреса не заблокована")
	}
	delete(s.cfg.Bans, key)
	delete(s.fails, key)
	s.Log.Printf("адресу %s розблоковано вручну", key)
	return s.save()
}

// SetSecurity changes the ban length, code lifetime and auto-pinning.
func (s *Server) SetSecurity(banHours, codeDays int, autoPin bool) error {
	if !containsInt(BanChoices, banHours) {
		return errors.New("невідомий термін блокування")
	}
	if !containsInt(CodeChoices, codeDays) {
		return errors.New("невідомий термін дії коду")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.BanHours, s.cfg.CodeTTLDays, s.cfg.AutoPin = banHours, codeDays, autoPin
	return s.save()
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// DismissAlert removes one "new address" alert.
func (s *Server) DismissAlert(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range s.cfg.Alerts {
		if a.ID == id {
			s.cfg.Alerts = append(s.cfg.Alerts[:i], s.cfg.Alerts[i+1:]...)
			return s.save()
		}
	}
	return errors.New("попередження вже немає")
}

func (s *Server) alertByIDLocked(id string) *Alert {
	for _, a := range s.cfg.Alerts {
		if a.ID == id {
			return a
		}
	}
	return nil
}
