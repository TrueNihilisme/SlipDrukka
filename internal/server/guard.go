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
//     in total;
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
	failMax        = 5
	failWindow     = 10 * time.Minute
	knownFor       = 30 * 24 * time.Hour
	maxPendingIP   = 4
	maxPendingAll  = 64
	maxBans        = 5000
	maxAlerts      = 50
	maxSeen        = 10
	defaultBanH    = 120 // 5 days
	defaultCodeD   = 7
	foreverSetting = -1
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
	if t, ok := s.cfg.KnownIPs[ip.String()]; ok && now.Sub(t) < knownFor {
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
	return s.banLocked(ip.String(), now) != nil || s.csBanned(ip)
}

func (s *Server) noteBannedHit(ip netip.Addr) {
	s.mu.Lock()
	if b := s.cfg.Bans[ip.String()]; b != nil {
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
	key := ip.String()
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
	key := ip.String()
	now := time.Now()
	var logs []string
	pinned := false
	s.mu.Lock()
	a := s.findAgentLocked(id)
	if a == nil {
		s.mu.Unlock()
		return
	}
	for k, t := range s.cfg.KnownIPs {
		if now.Sub(t) > knownFor {
			delete(s.cfg.KnownIPs, k)
		}
	}
	s.cfg.KnownIPs[key] = now
	delete(s.fails, key)
	if s.cfg.Bans[key] != nil {
		delete(s.cfg.Bans, key)
		logs = append(logs, fmt.Sprintf("адресу %s розблоковано: точка «%s» підключилась з дійсним кодом", key, a.Name))
	}
	a.CodeUsed = true
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
	} else { // keep the latest address last
		out := a.Seen[:0]
		for _, x := range a.Seen {
			if x != key {
				out = append(out, x)
			}
		}
		a.Seen = append(out, key)
	}
	err := s.save()
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
func (s *Server) enterHandshake(ip netip.Addr, banned bool) (release func(), ok bool) {
	key := ip.String()
	per := maxPendingIP
	if banned {
		per = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendAll >= maxPendingAll || s.pending[key] >= per {
		return nil, false
	}
	s.pendAll++
	s.pending[key]++
	return func() {
		s.mu.Lock()
		s.pendAll--
		if s.pending[key]--; s.pending[key] <= 0 {
			delete(s.pending, key)
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

// Unban lifts the block of one address.
func (s *Server) Unban(ip string) error {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return errors.New("некоректна адреса")
	}
	key := a.Unmap().String()
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
