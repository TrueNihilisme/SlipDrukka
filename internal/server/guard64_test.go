package server

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
)

// IPv6: failures, bans and handshake slots count a /64 network as one
// address, so changing the address inside it gives nothing.
func TestIPv6CountedByNetwork(t *testing.T) {
	s, _ := newGuardServer(t)
	in := func(i int) netip.Addr { return netip.MustParseAddr(fmt.Sprintf("2001:db8:1:2::%x", i+1)) }
	other := netip.MustParseAddr("2001:db8:1:3::1")
	for i := 0; i < failMax; i++ {
		s.noteFail(in(i), "тест", csUnknownCode)
	}
	if !s.isBanned(in(100)) {
		t.Fatal("5 невдач з різних адрес однієї мережі /64 не заблокували мережу")
	}
	if s.isBanned(other) {
		t.Fatal("заблоковано сусідню мережу /64")
	}
	s.mu.Lock()
	bans := s.bansLocked(time.Now())
	s.mu.Unlock()
	if len(bans) != 1 || bans[0].IP != "2001:db8:1:2::/64" {
		t.Fatalf("у списку заблокованих: %+v", bans)
	}
	if err := s.Unban(bans[0].IP); err != nil {
		t.Fatal(err)
	}
	if s.isBanned(in(100)) {
		t.Fatal("розблокування мережі не спрацювало")
	}

	for i := 0; i < maxPendingIP; i++ {
		if _, ok := s.enterHandshake(in(i), false); !ok {
			t.Fatalf("з'єднання %d відкинуто до межі", i)
		}
	}
	if _, ok := s.enterHandshake(in(200), false); ok {
		t.Fatal("нова адреса тієї самої мережі /64 отримала місце понад межу")
	}
	if _, ok := s.enterHandshake(other, false); !ok {
		t.Fatal("сусідня мережа /64 не отримала місця")
	}
}

// A ban written by a version that counted single IPv6 addresses covers the
// /64 network after an upgrade.
func TestOldIPv6BanRekeyed(t *testing.T) {
	s, dir := newGuardServer(t)
	s.mu.Lock()
	s.cfg.Bans["2001:db8:5:6::77"] = &Ban{Since: time.Now(), Reason: "тест"}
	s.cfg.KnownIPs["2001:db8:7:8::1"] = time.Now()
	err := s.save()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := New(dir, common.NewLogRing("", 10), nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	if !s2.isBanned(netip.MustParseAddr("2001:db8:5:6::1")) {
		t.Fatal("старе блокування адреси IPv6 не перейшло на мережу /64")
	}
	if !s2.exempt(netip.MustParseAddr("2001:db8:7:8::99")) {
		t.Fatal("стара відома адреса IPv6 не перейшла на мережу /64")
	}
}

// Addresses nobody knows cannot take every slot: the rest stays for
// addresses of known shops, and WireGuard connections have their own total.
func TestSlotsKeptForKnownShops(t *testing.T) {
	s, _ := newGuardServer(t)
	known := netip.MustParseAddr("192.0.2.44")
	s.mu.Lock()
	s.cfg.KnownIPs[guardKey(known)] = time.Now()
	s.mu.Unlock()

	got := 0
	for i := 0; i < 4*maxPendingAll; i++ { // every connection from another /64
		ip := netip.MustParseAddr(fmt.Sprintf("2001:db8:%x::1", i+1))
		if _, ok := s.enterHandshake(ip, false); ok {
			got++
		}
	}
	if got != maxPendingNew {
		t.Fatalf("чужі адреси зайняли %d місць, межа %d", got, maxPendingNew)
	}
	var rel []func()
	for i := 0; i < maxPendingIP; i++ {
		r, ok := s.enterHandshake(known, false)
		if !ok {
			t.Fatalf("відома адреса не отримала місця %d при зайнятих чужими", i)
		}
		rel = append(rel, r)
	}
	if _, ok := s.enterHandshakeWG(netip.MustParseAddr("198.51.100.200")); !ok {
		t.Fatal("WireGuard не отримав місця при зайнятих місцях TCP")
	}
	for _, r := range rel {
		r()
	}
	s.mu.Lock()
	all := s.pendAll
	s.mu.Unlock()
	if all != maxPendingNew {
		t.Fatalf("після звільнення зайнято %d, мало бути %d", all, maxPendingNew)
	}
}

// A connection from an unknown address that says nothing is closed after
// helloWait, not after the longer wait known addresses get.
func TestSilentConnectionClosedSoon(t *testing.T) {
	oldNew, oldKnown := helloWait, helloWaitKnown
	helloWait, helloWaitKnown = 300*time.Millisecond, 30*time.Second
	defer func() { helloWait, helloWaitKnown = oldNew, oldKnown }()
	s, _ := newGuardServer(t)
	if err := s.listenAgents("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer s.agentLn.Close()
	c, err := net.Dial("tcp", s.agentLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("сервер щось надіслав мовчазному з'єднанню")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("мовчазне з'єднання трималось %v", d)
	}
	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.pendAll == 0
	}, "місце мовчазного з'єднання не звільнилось")
}

// A shop reconnecting from the same address does not make the server write
// its config again; the "last seen" time is refreshed once an hour.
func TestReconnectDoesNotRewriteConfig(t *testing.T) {
	s, _ := newGuardServer(t)
	id, _ := addPoint(t, s, "Каса")
	s.noteGood(id, ipA)
	path := s.cfgPath()
	const mark = "mark"
	if err := os.WriteFile(path, []byte(mark), 0o600); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for i := 0; i < 3; i++ {
		s.noteGood(id, ipA)
	}
	if read() != mark {
		t.Fatal("повторне підключення з тієї самої адреси переписало конфіг")
	}
	s.mu.Lock()
	s.cfg.KnownIPs[guardKey(ipA)] = time.Now().Add(-knownRefresh - time.Minute)
	s.mu.Unlock()
	s.noteGood(id, ipA)
	if read() == mark {
		t.Fatal("час відомої адреси не оновлено після години")
	}
	if err := os.WriteFile(path, []byte(mark), 0o600); err != nil {
		t.Fatal(err)
	}
	s.noteGood(id, ipB) // a new address is a change
	if read() == mark {
		t.Fatal("нову адресу точки не збережено")
	}
}

// Damaged certificate files stop the server with a message in the log; a
// new certificate is never made over them.
func TestDamagedCertificateStopsServer(t *testing.T) {
	_, dir := newGuardServer(t)
	crt := common.Join(dir, "server.crt")
	if err := os.WriteFile(crt, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := common.NewLogRing("", 10)
	if _, err := New(dir, log, nopAdmin{}); err == nil {
		t.Fatal("сервер запустився з пошкодженим сертифікатом")
	}
	if b, _ := os.ReadFile(crt); string(b) != "broken" {
		t.Fatal("пошкоджений файл сертифіката перезаписано")
	}
	if !strings.Contains(strings.Join(log.Lines(), "\n"), "сертифікат сервера пошкоджено") {
		t.Fatalf("у журналі немає пояснення: %v", log.Lines())
	}
}

// A WireGuard key file that is there but cannot be read stops the server;
// the key is never replaced over it.
func TestUnreadableWGKeyStopsServer(t *testing.T) {
	_, dir := newGuardServer(t)
	key := common.Join(dir, "wg.key")
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(key, 0o700); err != nil { // reading a folder fails, and not with "no such file"
		t.Fatal(err)
	}
	log := common.NewLogRing("", 10)
	if _, err := New(dir, log, nopAdmin{}); err == nil {
		t.Fatal("сервер запустився з нечитабельним ключем WireGuard")
	}
	if fi, err := os.Stat(key); err != nil || !fi.IsDir() {
		t.Fatal("файл ключа WireGuard замінено")
	}
	if !strings.Contains(strings.Join(log.Lines(), "\n"), "wg.key") {
		t.Fatalf("у журналі немає пояснення: %v", log.Lines())
	}
}

// A name typed into the sign-in form cannot add lines to the log.
func TestLogNameOneLine(t *testing.T) {
	got := logName("admin\r\n2026-01-01 00:00:00 вхід у панель: root\u2028x")
	if strings.ContainsAny(got, "\r\n\u2028") {
		t.Fatalf("у назві лишився перехід рядка: %q", got)
	}
	long := logName(strings.Repeat("я", 500))
	if n := len([]rune(long)); n != 65 {
		t.Fatalf("довга назва: %d знаків, мало бути 64 і три крапки", n)
	}
}

// Deleting a point also ends its delivery worker and drops what the server
// kept for it.
func TestDeleteAgentCleansUp(t *testing.T) {
	s, _ := newGuardServer(t)
	id, _ := addPoint(t, s, "Каса")
	s.wake(id)
	s.mu.Lock()
	w := s.workers[id]
	s.events[id] = []proto.Event{{Kind: "missing"}}
	s.stats[portKey(id, "p")] = &printerStats{}
	s.mu.Unlock()
	if w == nil {
		t.Fatal("робочий цикл доставки не створено")
	}
	if err := s.DeleteAgent(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.stop:
	default:
		t.Fatal("робочий цикл доставки не зупинено")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.workers) != 0 || len(s.events) != 0 || len(s.stats) != 0 {
		t.Fatalf("лишилось: цикли %d, події %d, лічильники %d", len(s.workers), len(s.events), len(s.stats))
	}
}
