package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The line format must match crowdsec/parsers (the grok patterns there).
func TestCrowdSecLogFormat(t *testing.T) {
	s, dir := newGuardServer(t)
	s.noteFail(ipA, "тест", csUnknownCode) // log off: nothing written
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(dir + "/crowdsec.log"); err == nil {
		t.Fatal("журнал для CrowdSec вимкнено, а файл з'явився")
	}
	if err := s.SetCrowdSec(true, false, "", ""); err != nil {
		t.Fatal(err)
	}
	s.noteFail(ipA, "тест", csUnknownCode)
	s.mu.Lock()
	s.cfg.KnownIPs[ipB.String()] = time.Now()
	s.mu.Unlock()
	s.noteFail(ipB, "тест", csHandshake) // a known shop: never logged
	var b []byte
	waitFor(t, func() bool { b, _ = os.ReadFile(dir + "/crowdsec.log"); return len(b) > 0 }, "рядок не записано")
	time.Sleep(50 * time.Millisecond)
	b, _ = os.ReadFile(dir + "/crowdsec.log")
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("рядків %d, мав бути 1 (відома адреса не пишеться): %q", len(lines), b)
	}
	re := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d[+-]\d\d:\d\d slipdrukka failed-connection src=198\.51\.100\.7 reason=unknown-code$`)
	if !re.MatchString(lines[0]) {
		t.Fatalf("формат рядка: %q", lines[0])
	}
}

// Decisions from the Local API stream block addresses, ranges included;
// deleted decisions unblock; exempt addresses are never blocked; the key is
// sent in X-Api-Key and never shown back.
func TestCrowdSecDecisions(t *testing.T) {
	var round atomic.Int32
	var gotKey atomic.Value
	lapi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey.Store(r.Header.Get("X-Api-Key"))
		if r.URL.Path != "/v1/decisions/stream" {
			w.WriteHeader(404)
			return
		}
		var out csStream
		if r.URL.Query().Get("startup") == "true" {
			out.New = []csDecision{{Scope: "Ip", Type: "ban", Value: "192.0.2.10"}, {Scope: "Range", Type: "ban", Value: "203.0.113.0/24"},
				{Scope: "ip", Type: "captcha", Value: "192.0.2.11"}, {Scope: "Country", Type: "ban", Value: "XX"}}
		} else if round.Add(1) == 1 {
			out.Deleted = []csDecision{{Scope: "Ip", Type: "ban", Value: "192.0.2.10"}}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer lapi.Close()
	old := csPollEvery
	csPollEvery = 200 * time.Millisecond
	defer func() { csPollEvery = old }()

	s, _ := newGuardServer(t)
	if err := s.SetCrowdSec(false, true, lapi.URL, ""); err == nil {
		t.Fatal("без ключа мало відмовити")
	}
	if err := s.SetCrowdSec(false, true, lapi.URL, "secret-key-1"); err != nil {
		t.Fatal(err)
	}
	defer s.SetCrowdSec(false, false, lapi.URL, "")
	a := netip.MustParseAddr
	waitFor(t, func() bool { return s.isBanned(a("192.0.2.10")) }, "бан з CrowdSec не застосовано")
	if !s.isBanned(a("203.0.113.55")) {
		t.Fatal("бан діапазону не застосовано")
	}
	if s.isBanned(a("192.0.2.11")) {
		t.Fatal("captcha — не бан")
	}
	s.mu.Lock()
	s.cfg.KnownIPs["203.0.113.9"] = time.Now()
	s.mu.Unlock()
	if s.isBanned(a("203.0.113.9")) {
		t.Fatal("магазин за забаненою адресою відрізано")
	}
	waitFor(t, func() bool { return !s.isBanned(a("192.0.2.10")) }, "видалене рішення не зняло бан")
	if k, _ := gotKey.Load().(string); k != "secret-key-1" {
		t.Fatalf("ключ у заголовку: %q", k)
	}
	v := s.crowdSecView()
	b, _ := json.Marshal(v)
	if !v.HasKey || strings.Contains(string(b), "secret-key-1") {
		t.Fatalf("стан для панелі: %s", b)
	}
	if v.LastSync.IsZero() || v.Error != "" {
		t.Fatalf("стан синхронізації: %+v", v)
	}
}

// A wrong key is reported, not silently ignored.
func TestCrowdSecWrongKey(t *testing.T) {
	lapi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer lapi.Close()
	old := csPollEvery
	csPollEvery = 200 * time.Millisecond
	defer func() { csPollEvery = old }()
	s, _ := newGuardServer(t)
	if err := s.SetCrowdSec(false, true, lapi.URL, "bad"); err != nil {
		t.Fatal(err)
	}
	defer s.SetCrowdSec(false, false, lapi.URL, "")
	waitFor(t, func() bool { return strings.Contains(s.crowdSecView().Error, "ключ") }, "помилку ключа не показано")
}
