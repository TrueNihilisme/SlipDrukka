package server

// Optional CrowdSec integration (crowdsec/ in the repository has the rules):
//
//   - a log for CrowdSec: every counted failed connection to the points port
//     becomes one line in crowdsec.log, which CrowdSec reads with the shipped
//     parser and scenario and blocks through its own firewall component;
//   - asking CrowdSec: the server pulls the Local API decisions stream (like
//     any remediation component) and drops banned addresses at accept, with
//     the same exemptions as its own bans (allow-lists, shops seen in the
//     last 30 days), so a shop behind a shared address is never cut off.
//
// The API key is a secret: it lives in crowdsec.key (0600) next to the
// config, is never written to logs and never sent back to the panel.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"slipdrukka/internal/common"
)

// csPollEvery: how often decisions are pulled (a variable for tests).
var csPollEvery = 10 * time.Second

const (
	csLogMax = 5 << 20 // crowdsec.log is rotated to .1 past this

	csMaxBody      = 64 << 20
	csMaxDecisions = 200000
)

// csTime: always a numeric offset (+00:00 in UTC, never "Z"): the form the
// shipped CrowdSec parser was verified with.
const csTime = "2006-01-02T15:04:05-07:00"

// csFailCode is the stable, ASCII reason written for CrowdSec.
type csFailCode string

const (
	csHandshake   csFailCode = "bad-handshake"
	csUnknownCode csFailCode = "unknown-code"
	csExpiredCode csFailCode = "expired-code"
)

type CrowdSecView struct {
	Log      bool      `json:"log"`
	LogPath  string    `json:"log_path"`
	Ask      bool      `json:"ask"`
	URL      string    `json:"url"`
	HasKey   bool      `json:"has_key"`
	LastSync time.Time `json:"last_sync"`
	Banned   int       `json:"banned"`
	Error    string    `json:"error,omitempty"`
}

// csState is what the last pull from CrowdSec said.
type csState struct {
	mu       sync.Mutex
	ips      map[netip.Addr]bool
	ranges   map[netip.Prefix]bool
	lastSync time.Time
	err      string
	stop     context.CancelFunc
}

func (s *Server) csKeyPath() string { return common.Join(s.dir, "crowdsec.key") }
func (s *Server) csLogPath() string { return common.Join(s.dir, "crowdsec.log") }

func (s *Server) csKey() string {
	b, err := os.ReadFile(s.csKeyPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// csWriteLog appends one failed connection for CrowdSec, e.g.
//
//	2026-11-13T09:53:00+02:00 slipdrukka failed-connection src=192.0.2.77 reason=unknown-code
func (s *Server) csWriteLog(ip netip.Addr, code csFailCode) {
	s.mu.Lock()
	on := s.cfg.CrowdSecLog
	s.mu.Unlock()
	if !on {
		return
	}
	s.csLogMu.Lock()
	defer s.csLogMu.Unlock()
	path := s.csLogPath()
	if st, err := os.Stat(path); err == nil && st.Size() > csLogMax {
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s slipdrukka failed-connection src=%s reason=%s\n", time.Now().Format(csTime), ip, code)
}

// csBanned: CrowdSec has a ban for ip (exemptions are checked by the caller).
func (s *Server) csBanned(ip netip.Addr) bool {
	st := &s.cs
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.ips[ip] {
		return true
	}
	for p := range st.ranges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// startCrowdSec (re)starts or stops pulling decisions per the settings.
func (s *Server) startCrowdSec() {
	s.mu.Lock()
	ask, base := s.cfg.CrowdSecAsk, s.cfg.CrowdSecURL
	s.mu.Unlock()
	st := &s.cs
	st.mu.Lock()
	if st.stop != nil {
		st.stop()
		st.stop = nil
	}
	st.ips, st.ranges, st.err = map[netip.Addr]bool{}, map[netip.Prefix]bool{}, ""
	st.lastSync = time.Time{}
	if !ask || base == "" {
		st.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.stop = cancel
	st.mu.Unlock()
	go s.pullCrowdSec(ctx, base, csPollEvery)
}

type csDecision struct {
	Scope string `json:"scope"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type csStream struct {
	New     []csDecision `json:"new"`
	Deleted []csDecision `json:"deleted"`
}

func (s *Server) pullCrowdSec(ctx context.Context, base string, every time.Duration) {
	client := &http.Client{Timeout: 30 * time.Second}
	startup := true
	logged := ""
	for {
		err := s.pullOnce(ctx, client, base, startup)
		st := &s.cs
		st.mu.Lock()
		if err == nil {
			startup = false
			st.err, st.lastSync = "", time.Now()
		} else {
			st.err = err.Error()
		}
		st.mu.Unlock()
		if err != nil && err.Error() != logged && ctx.Err() == nil {
			s.Log.Printf("CrowdSec: %v", err)
		}
		if err == nil && logged != "" {
			s.Log.Printf("CrowdSec: зв'язок відновлено")
		}
		if err != nil {
			logged = err.Error()
			startup = true // resync everything once it answers again
		} else {
			logged = ""
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

func (s *Server) pullOnce(ctx context.Context, client *http.Client, base string, startup bool) error {
	key := s.csKey()
	if key == "" {
		return errors.New("не задано ключ API")
	}
	u := strings.TrimRight(base, "/") + "/v1/decisions/stream?startup=" + fmt.Sprint(startup)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("User-Agent", "slipdrukka/"+s.Version)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("немає зв'язку з %s", base)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return errors.New("CrowdSec не прийняв ключ API")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CrowdSec відповів %d", resp.StatusCode)
	}
	var data csStream
	if err := json.NewDecoder(io.LimitReader(resp.Body, csMaxBody)).Decode(&data); err != nil {
		return errors.New("незрозуміла відповідь CrowdSec")
	}
	st := &s.cs
	st.mu.Lock()
	defer st.mu.Unlock()
	if startup {
		st.ips, st.ranges = map[netip.Addr]bool{}, map[netip.Prefix]bool{}
	}
	for _, d := range data.Deleted {
		if a, p, ok := csParse(d); ok {
			if p.IsValid() {
				delete(st.ranges, p)
			} else {
				delete(st.ips, a)
			}
		}
	}
	for _, d := range data.New {
		if len(st.ips)+len(st.ranges) >= csMaxDecisions {
			break
		}
		if a, p, ok := csParse(d); ok {
			if p.IsValid() {
				st.ranges[p] = true
			} else {
				st.ips[a] = true
			}
		}
	}
	return nil
}

// csParse reads one ban decision; only scopes ip and range, type ban.
func csParse(d csDecision) (netip.Addr, netip.Prefix, bool) {
	if !strings.EqualFold(d.Type, "ban") {
		return netip.Addr{}, netip.Prefix{}, false
	}
	switch strings.ToLower(d.Scope) {
	case "ip":
		a, err := netip.ParseAddr(d.Value)
		if err != nil {
			return netip.Addr{}, netip.Prefix{}, false
		}
		return a.Unmap(), netip.Prefix{}, true
	case "range":
		p, err := netip.ParsePrefix(d.Value)
		if err != nil || p.Bits() == 0 {
			return netip.Addr{}, netip.Prefix{}, false
		}
		return netip.Addr{}, p.Masked(), true
	}
	return netip.Addr{}, netip.Prefix{}, false
}

func (s *Server) crowdSecView() CrowdSecView {
	s.mu.Lock()
	v := CrowdSecView{Log: s.cfg.CrowdSecLog, LogPath: s.csLogPath(), Ask: s.cfg.CrowdSecAsk, URL: s.cfg.CrowdSecURL}
	s.mu.Unlock()
	v.HasKey = s.csKey() != ""
	st := &s.cs
	st.mu.Lock()
	v.LastSync, v.Banned, v.Error = st.lastSync, len(st.ips)+len(st.ranges), st.err
	st.mu.Unlock()
	return v
}

// SetCrowdSec changes the integration. key "" keeps the stored key.
func (s *Server) SetCrowdSec(logOn, ask bool, base, key string) error {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("адреса API CrowdSec — у вигляді http://127.0.0.1:8080")
	}
	key = strings.TrimSpace(key)
	if key != "" {
		if len(key) > 512 || strings.ContainsAny(key, " \r\n\t") {
			return errors.New("некоректний ключ API")
		}
		if err := os.WriteFile(s.csKeyPath(), []byte(key+"\n"), 0o600); err != nil {
			return err
		}
	}
	if ask && s.csKey() == "" {
		return errors.New("щоб питати CrowdSec, задайте ключ API (cscli bouncers add slipdrukka)")
	}
	s.mu.Lock()
	s.cfg.CrowdSecLog, s.cfg.CrowdSecAsk, s.cfg.CrowdSecURL = logOn, ask, base
	err = s.save()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.startCrowdSec()
	return nil
}
