package server

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
)

func newGuardServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir, common.NewLogRing("", 200), nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPublicAddr("127.0.0.1:7310"); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func addPoint(t *testing.T, s *Server, name string) (id, key string) {
	t.Helper()
	id, code, err := s.AddAgent(name)
	if err != nil {
		t.Fatal(err)
	}
	c, err := proto.ParseCode(code)
	if err != nil {
		t.Fatal(err)
	}
	return id, c.Key
}

var (
	ipA = netip.MustParseAddr("198.51.100.7")
	ipB = netip.MustParseAddr("203.0.113.9")
)

func failN(s *Server, ip netip.Addr, n int) {
	for i := 0; i < n; i++ {
		s.noteFail(ip, "тест", csUnknownCode)
	}
}

// Five failures in ten minutes block the address for the default 5 days;
// four do not.
func TestBanAfterFailures(t *testing.T) {
	s, _ := newGuardServer(t)
	failN(s, ipA, failMax-1)
	if s.isBanned(ipA) {
		t.Fatal("заблоковано раніше за поріг")
	}
	s.noteFail(ipA, "тест", csUnknownCode)
	if !s.isBanned(ipA) {
		t.Fatal("не заблоковано після 5 невдач")
	}
	s.mu.Lock()
	b := s.cfg.Bans[ipA.String()]
	s.mu.Unlock()
	if d := time.Until(b.Until); d < 119*time.Hour || d > 121*time.Hour {
		t.Fatalf("термін блокування %v, мало бути 5 днів", d)
	}
	if s.isBanned(ipB) {
		t.Fatal("заблоковано чужу адресу")
	}
}

// Old failures fall out of the window.
func TestBanWindow(t *testing.T) {
	s, _ := newGuardServer(t)
	s.mu.Lock()
	old := time.Now().Add(-failWindow - time.Minute)
	s.fails[ipA.String()] = []time.Time{old, old, old, old}
	s.mu.Unlock()
	s.noteFail(ipA, "тест", csUnknownCode)
	if s.isBanned(ipA) {
		t.Fatal("старі невдачі мали не рахуватися")
	}
}

// Allow-listed, point-pinned and recently good addresses are never banned.
func TestBanExemptions(t *testing.T) {
	s, _ := newGuardServer(t)
	id, _ := addPoint(t, s, "Каса")
	if err := s.SetAllowed("198.51.100.0/24", true); err != nil {
		t.Fatal(err)
	}
	failN(s, ipA, 10)
	if s.isBanned(ipA) {
		t.Fatal("заблоковано адресу зі списку дозволених")
	}
	if err := s.SetAllowed("", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentAllowed(id, ipB.String()); err != nil {
		t.Fatal(err)
	}
	failN(s, ipB, 10)
	if s.isBanned(ipB) {
		t.Fatal("заблоковано адресу, прив'язану до точки")
	}
	known := netip.MustParseAddr("192.0.2.44")
	s.mu.Lock()
	s.cfg.KnownIPs[known.String()] = time.Now().Add(-24 * time.Hour)
	s.mu.Unlock()
	failN(s, known, 10)
	if s.isBanned(known) {
		t.Fatal("заблоковано адресу, з якої недавно підключалась точка")
	}
	stale := netip.MustParseAddr("192.0.2.45")
	s.mu.Lock()
	s.cfg.KnownIPs[stale.String()] = time.Now().Add(-knownFor - time.Hour)
	s.mu.Unlock()
	failN(s, stale, failMax)
	if !s.isBanned(stale) {
		t.Fatal("адреса, відома понад 30 днів тому, мала блокуватися")
	}
}

// Bans survive a restart, expire on their own and can be lifted by hand;
// allowing an address lifts its ban too.
func TestBanPersistExpireUnban(t *testing.T) {
	s, dir := newGuardServer(t)
	failN(s, ipA, failMax)
	s2, err := New(dir, common.NewLogRing("", 10), nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	if !s2.isBanned(ipA) {
		t.Fatal("блокування не пережило перезапуск")
	}
	if err := s2.Unban(ipA.String()); err != nil {
		t.Fatal(err)
	}
	if s2.isBanned(ipA) {
		t.Fatal("розблокування не спрацювало")
	}
	failN(s2, ipA, failMax)
	s2.mu.Lock()
	s2.cfg.Bans[ipA.String()].Until = time.Now().Add(-time.Second)
	s2.mu.Unlock()
	if s2.isBanned(ipA) {
		t.Fatal("прострочене блокування ще діє")
	}
	failN(s2, ipB, failMax)
	if err := s2.SetAllowed(ipB.String(), true); err != nil {
		t.Fatal(err)
	}
	s2.mu.Lock()
	_, still := s2.cfg.Bans[ipB.String()]
	s2.mu.Unlock()
	if still {
		t.Fatal("адресу додано до дозволених, а блокування лишилось")
	}
}

func TestBanForeverAndSettings(t *testing.T) {
	s, _ := newGuardServer(t)
	if err := s.SetSecurity(5, 7, false); err == nil {
		t.Fatal("прийнято термін, якого нема в меню")
	}
	if err := s.SetSecurity(foreverSetting, foreverSetting, true); err != nil {
		t.Fatal(err)
	}
	failN(s, ipA, failMax)
	s.mu.Lock()
	b := s.cfg.Bans[ipA.String()]
	s.mu.Unlock()
	if b == nil || !b.Until.IsZero() {
		t.Fatal("мало бути блокування назавжди")
	}
}

// An unused code expires; a used one does not; a new code starts afresh;
// points made before this feature keep working.
func TestCodeExpiry(t *testing.T) {
	s, _ := newGuardServer(t)
	id, _ := addPoint(t, s, "Каса")
	s.mu.Lock()
	a := s.findAgentLocked(id)
	now := time.Now()
	if s.codeExpiredLocked(a, now) {
		t.Fatal("новий код уже протермінований")
	}
	if !s.codeExpiredLocked(a, now.Add(8*24*time.Hour)) {
		t.Fatal("невикористаний код мав протермінуватися за 7 днів")
	}
	a.CodeUsed = true
	if s.codeExpiredLocked(a, now.Add(400*24*time.Hour)) {
		t.Fatal("використаний код не має старіти")
	}
	legacy := &AgentConfig{ID: "x"}
	if s.codeExpiredLocked(legacy, now.Add(400*24*time.Hour)) {
		t.Fatal("код старої точки не має старіти")
	}
	s.mu.Unlock()
	if _, err := s.Rekey(id); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if a.CodeUsed || s.codeExpiredLocked(a, time.Now()) {
		t.Fatal("новий код мав початися заново")
	}
	s.mu.Unlock()
	if err := s.SetSecurity(defaultBanH, foreverSetting, false); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if s.codeExpiredLocked(a, time.Now().Add(400*24*time.Hour)) {
		t.Fatal("«без терміну» не діє")
	}
	s.mu.Unlock()
}

// A point from a new address raises an alert; with AutoPin the first
// address is tied to the code instead.
func TestNewAddressAlertAndAutoPin(t *testing.T) {
	s, _ := newGuardServer(t)
	id, _ := addPoint(t, s, "Каса")
	s.noteGood(id, ipA)
	s.noteGood(id, ipA)
	if n := len(s.State().Alerts); n != 0 {
		t.Fatalf("перша і та сама адреса дали %d попереджень", n)
	}
	s.noteGood(id, ipB)
	st := s.State()
	if len(st.Alerts) != 1 || st.Alerts[0].IP != ipB.String() || st.Alerts[0].Prev[0] != ipA.String() {
		t.Fatalf("попередження про нову адресу: %+v", st.Alerts)
	}
	if err := s.DismissAlert(st.Alerts[0].ID); err != nil || len(s.State().Alerts) != 0 {
		t.Fatal("попередження не прибралось")
	}

	if err := s.SetSecurity(defaultBanH, defaultCodeD, true); err != nil {
		t.Fatal(err)
	}
	id2, _ := addPoint(t, s, "Склад")
	s.noteGood(id2, ipB)
	s.mu.Lock()
	pinned := s.findAgentLocked(id2).AllowedIPs
	s.mu.Unlock()
	if len(pinned) != 1 || pinned[0] != ipB.String() {
		t.Fatalf("прив'язка до першої адреси: %v", pinned)
	}
}

// Connections that have not shown a code are limited per address and in
// total; a banned address gets one at a time.
func TestHandshakeLimits(t *testing.T) {
	s, _ := newGuardServer(t)
	var rel []func()
	for i := 0; i < maxPendingIP; i++ {
		r, ok := s.enterHandshake(ipA, false)
		if !ok {
			t.Fatalf("з'єднання %d відкинуто до межі", i)
		}
		rel = append(rel, r)
	}
	if _, ok := s.enterHandshake(ipA, false); ok {
		t.Fatal("понад межу на адресу пропущено")
	}
	rel[0]()
	if _, ok := s.enterHandshake(ipA, false); !ok {
		t.Fatal("після звільнення місце не повернулось")
	}
	if r, ok := s.enterHandshake(ipB, true); !ok {
		t.Fatal("заблокована адреса не отримала жодного місця")
	} else if _, ok := s.enterHandshake(ipB, true); ok {
		t.Fatal("заблокованій адресі дано друге місце")
	} else {
		r()
	}
	s.mu.Lock()
	s.pendAll = maxPendingAll
	s.mu.Unlock()
	if _, ok := s.enterHandshake(netip.MustParseAddr("192.0.2.1"), false); ok {
		t.Fatal("понад загальну межу пропущено")
	}
}

// The whole path over a real TLS listener: garbage and wrong codes get the
// address banned, a banned address is closed silently, and a valid code from
// it gets through and lifts the ban.
func TestBanOverNetwork(t *testing.T) {
	s, _ := newGuardServer(t)
	_, key := addPoint(t, s, "Каса")
	if err := s.listenAgents("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer s.agentLn.Close()
	addr := s.agentLn.Addr().String()
	lo := netip.MustParseAddr("127.0.0.1")

	hello := func(k string) (proto.Welcome, error) {
		c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			return proto.Welcome{}, err
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		if err := proto.WriteLine(c, proto.Hello{Proto: proto.Version, Key: k, Hostname: "t"}); err != nil {
			return proto.Welcome{}, err
		}
		var w proto.Welcome
		err = proto.ReadLine(c, &w)
		return w, err
	}
	// garbage: plain bytes instead of TLS
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		c.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
		c.Close()
	}
	for i := 0; i < 3; i++ {
		w, err := hello("wrong-key")
		if err != nil || w.OK {
			t.Fatalf("чужий код: %+v %v", w, err)
		}
	}
	waitFor(t, func() bool { return s.isBanned(lo) }, "адресу не заблоковано після сміття і чужих кодів")

	// banned: wrong code gets no answer at all
	if w, err := hello("wrong-key"); err == nil {
		t.Fatalf("заблокована адреса отримала відповідь: %+v", w)
	}
	// banned: a valid code gets through and lifts the ban
	w, err := hello(key)
	if err != nil || !w.OK {
		t.Fatalf("дійсний код із заблокованої адреси: %+v %v", w, err)
	}
	waitFor(t, func() bool { return !s.isBanned(lo) }, "дійсний код не зняв блокування")
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// Admin actions land in audit.log with who and from where, and survive a
// restart.
func TestAuditLog(t *testing.T) {
	s, dir := newGuardServer(t)
	CheckLogin = func(user, pw string) error { return nil }
	h := s.routes()
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"user":"admin","password":"x"}`))
	r.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("вхід: %d", w.Code)
	}
	cookie := w.Result().Cookies()[0]
	r = httptest.NewRequest("POST", "/api/agents", strings.NewReader(`{"name":"Склад"}`))
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-SD", "1")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("додати точку: %d %s", w.Code, w.Body)
	}
	b, err := os.ReadFile(dir + "/audit.log")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var last AuditEntry
	json.Unmarshal([]byte(lines[len(lines)-1]), &last)
	if last.Who != "admin" || last.From != "127.0.0.1" || last.Action != "додано точку" || last.Detail != "Склад" {
		t.Fatalf("запис журналу: %+v", last)
	}
	s2, err := New(dir, common.NewLogRing("", 10), nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.audit.recent(10); len(got) != len(lines) || got[0].Action != "додано точку" {
		t.Fatalf("після перезапуску: %+v", got)
	}
}
