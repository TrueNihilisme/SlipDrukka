// Package server is the part that runs next to Windows Server. Agents dial
// in; every printer an agent offers becomes a local RAW port
// (127.0.0.1:<port>) that a normal Windows printer queue can point at.
package server

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"

	"slipdrukka/internal/common"
	"slipdrukka/internal/i18n"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/tlsutil"
	"slipdrukka/internal/wgtun"
	"slipdrukka/internal/winauth"
)

type Config struct {
	Listen       string `json:"listen"`      // where agents connect, e.g. ":7310"
	Web          string `json:"web"`         // admin page, e.g. "127.0.0.1:7330"
	PublicAddr   string `json:"public_addr"` // host:port written into codes
	FirstPort    int    `json:"first_port"`  // first local RAW port
	PasswordHash string `json:"password_hash"`
	// EmergencyLAN: the emergency password is accepted from other computers
	// too (0.17.0). Off by default: over plain HTTP it travels in clear text.
	EmergencyLAN bool           `json:"emergency_lan,omitempty"`
	Agents       []*AgentConfig `json:"agents"`
	Ports        map[string]int `json:"ports"` // "<agentID>/<alias>" -> local port
	// Allowed: source addresses (IP or CIDR) points may connect from.
	// Empty means any. Enforced by the firewall rule and by the server itself.
	Allowed []string `json:"allowed,omitempty"`
	// JobTTLMinutes: undelivered jobs older than this are dropped (0 = 60).
	JobTTLMinutes int `json:"job_ttl_minutes"`

	// Protection of the points port (guard.go).
	// BanHours: how long an address stays blocked after repeated failed
	// connections (0 = default 120 h, -1 = until removed by hand).
	BanHours int `json:"ban_hours,omitempty"`
	// CodeTTLDays: a connection code nobody has used expires after this many
	// days (0 = default 7, -1 = never).
	CodeTTLDays int `json:"code_ttl_days,omitempty"`
	// AutoPin ties a point's code to the first address it connects from.
	AutoPin bool `json:"auto_pin,omitempty"`
	// Bans: blocked source addresses.
	Bans map[string]*Ban `json:"bans,omitempty"`
	// KnownIPs: addresses a point connected from, with the last time. They
	// are never banned (a shop whose point has an old code keeps knocking).
	KnownIPs map[string]time.Time `json:"known_ips,omitempty"`
	// Alerts: points seen from a new address, until the admin dismisses them.
	Alerts []*Alert `json:"alerts,omitempty"`
	// WGMode: the WireGuard channel for points (wg.go): "" on, "off", "only".
	WGMode string `json:"wg_mode,omitempty"`
	// CrowdSec integration (crowdsec.go); the API key is in crowdsec.key.
	CrowdSecLog bool   `json:"crowdsec_log,omitempty"`
	CrowdSecAsk bool   `json:"crowdsec_ask,omitempty"`
	CrowdSecURL string `json:"crowdsec_url,omitempty"`
	// Lang: the language of the log files ("uk" or "en"). A new install
	// takes the Windows display language; configs from before 0.13 have
	// none and stay Ukrainian.
	Lang string `json:"lang,omitempty"`
}

type AgentConfig struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	KeyHash string `json:"key_hash"`
	// AllowedIPs ties this point's code to its addresses (optional).
	AllowedIPs []string  `json:"allowed_ips,omitempty"`
	Created    time.Time `json:"created"`
	// CodeIssued: when the current code was made; zero for points made
	// before codes could expire (their codes never do).
	CodeIssued time.Time `json:"code_issued,omitempty"`
	// CodeUsed: a point has connected with the current code.
	CodeUsed bool `json:"code_used,omitempty"`
	// One-time codes (0.18.0). OwnKey: the point connects with its own key
	// from the server, so its connection code no longer works. PendingKey:
	// that key, issued but not yet confirmed by a connection with it; kept
	// until then so it can be sent again if the point did not save it.
	OwnKey     bool   `json:"own_key,omitempty"`
	PendingKey string `json:"pending_key,omitempty"`
	// Seen: addresses the point connected from, newest last.
	Seen []string `json:"seen,omitempty"`
	// WGPub: the point's WireGuard public key, derived from its code secret
	// (known once the code was issued by 0.10+ or the point connected).
	WGPub string `json:"wg_pub,omitempty"`
}

type agentConn struct {
	id, remote, hostname, version string
	via                           string // proto.ViaTCP or proto.ViaWireGuard
	since                         time.Time
	printers                      []proto.Printer
	sess                          *yamux.Session
	inv                           *proto.Inventory // last report from the point
	invAt                         time.Time
	seenAs                        string      // public address as the server sees it
	rtt                           []RTTSample // round trips over the connection, last hour (s.mu)
}

// RTTSample is one round trip to a point over its connection (yamux ping).
type RTTSample struct {
	At time.Time `json:"at"`
	MS float64   `json:"ms"`
}

// RTTEvery and rttKeep: how often a point is pinged and how many samples are
// kept (an hour).
var (
	RTTEvery = 30 * time.Second
	rttKeep  = 120
)

// measureRTT pings the point over its connection until the session ends.
func (s *Server) measureRTT(conn *agentConn) {
	t := time.NewTicker(RTTEvery)
	defer t.Stop()
	for {
		if d, err := conn.sess.Ping(); err == nil {
			s.mu.Lock()
			conn.rtt = append(conn.rtt, RTTSample{At: time.Now(), MS: float64(d.Microseconds()) / 1000})
			if len(conn.rtt) > rttKeep {
				conn.rtt = conn.rtt[len(conn.rtt)-rttKeep:]
			}
			s.mu.Unlock()
		}
		select {
		case <-conn.sess.CloseChan():
			return
		case <-t.C:
		}
	}
}

type printerStats struct {
	OK, Failed int
	LastJob    time.Time
	LastError  string
	LastBytes  int64
}

type Server struct {
	Version string
	dir     string
	Log     *common.LogRing
	cert    tls.Certificate
	admin   Admin // Windows printer management, faked off-Windows

	mu        sync.Mutex
	cfg       Config
	online    map[string]*agentConn
	listeners map[string]net.Listener
	stats     map[string]*printerStats
	sessions  *common.Sessions
	limiter   *common.Limiter

	qMu    sync.Mutex
	qCache map[string][]Queue
	qAt    time.Time

	agentLn net.Listener
	web     *common.WebServer
	// Firewall hooks, set by main on Windows; nil in tests.
	FirewallAdd    func(name, protocol string, port int, remote []string) error
	FirewallRemove func(name string)
	// FirewallRead reads rules by display name (Settings tip, fwtip.go).
	FirewallRead func(names ...string) ([]FWRule, error)
	fwMu         sync.Mutex
	fwAt         time.Time
	fwRules      []FWRule
	fwErr        error
	// LocalClient decides whether a connection to a local printer port may
	// deliver a job (nil error) or is refused (the error is the reason). Set
	// by main on Windows to admit only the print spooler; nil admits all.
	LocalClient func(c net.Conn) error
	// LoginKey names who is trying to sign in to the admin page; failed
	// attempts are counted per key. nil: the address of the browser.
	LoginKey  func(r *http.Request) string
	refusedAt time.Time // last log line about a refused local connection
	warning   string    // shown in the admin page (SetWarning)

	refused map[string]*refusedInfo // source IP -> refused connections
	fails   map[string][]time.Time  // source IP -> recent failed connections
	pending map[string]int          // source IP -> connections still in the handshake
	pendAll int                     // all connections still in the handshake
	audit   *auditLog

	wgKey wgtun.Key     // server's WireGuard private key (wg.key)
	wg    *wgtun.Server // nil when WireGuard is off
	wgLn  net.Listener  // TCP inside the tunnel
	// wgGrace: old WireGuard keys kept for a moment (retireWGKeyLocked).
	wgGrace map[string]time.Time

	cs       csState                  // CrowdSec decisions (crowdsec.go)
	csLogMu  sync.Mutex               // crowdsec.log writes
	lang     atomic.Value             // log language, read by the log without s.mu
	events   map[string][]proto.Event // point ID -> events it reported
	portless map[string]bool          // "<agentID>/<alias>" already reported as left without a port

	incoming atomic.Int64 // bytes of jobs being received right now (spool.go)
	// arriving: jobs still being received from Windows, per printer key:
	// order number -> when data last came (spool.go, holdsBack).
	arriving map[string]map[int64]time.Time

	ctx     context.Context
	jobs    map[string]*spoolJob     // queued jobs by ID
	seq     int64                    // arrival order
	workers map[string]chan struct{} // delivery worker per point
}

// New loads (or creates) the config in dir.
func New(dir string, log *common.LogRing, admin Admin) (*Server, error) {
	s := &Server{
		dir: dir, Log: log, admin: admin,
		online: map[string]*agentConn{}, listeners: map[string]net.Listener{},
		stats: map[string]*printerStats{},
		ctx:   context.Background(), jobs: map[string]*spoolJob{}, workers: map[string]chan struct{}{},
		refused: map[string]*refusedInfo{}, events: map[string][]proto.Event{}, portless: map[string]bool{},
		fails: map[string][]time.Time{}, pending: map[string]int{},
	}
	_, statErr := os.Stat(s.cfgPath())
	if err := common.LoadJSON(s.cfgPath(), &s.cfg); err != nil {
		return nil, fmt.Errorf("конфіг: %w", err)
	}
	if s.cfg.Lang == "" {
		if os.IsNotExist(statErr) {
			s.cfg.Lang = i18n.System()
		} else {
			s.cfg.Lang = i18n.UK
		}
	}
	s.lang.Store(s.cfg.Lang)
	log.Lang = s.logLang
	if s.cfg.Listen == "" {
		s.cfg.Listen = ":7310"
	}
	if s.cfg.Web == "" {
		s.cfg.Web = "127.0.0.1:7330"
	}
	if s.cfg.FirstPort == 0 {
		s.cfg.FirstPort = 9101
	}
	if s.cfg.Ports == nil {
		s.cfg.Ports = map[string]int{}
	}
	cert, err := tlsutil.LoadOrCreate(dir)
	if err != nil {
		return nil, fmt.Errorf("сертифікат: %w", err)
	}
	s.cert = cert
	if s.wgKey, err = loadWGKey(dir); err != nil {
		return nil, fmt.Errorf("ключ WireGuard: %w", err)
	}
	if err := s.loadSpoolLocked(); err != nil {
		return nil, fmt.Errorf("черга: %w", err)
	}
	if s.cfg.Bans == nil {
		s.cfg.Bans = map[string]*Ban{}
	}
	if s.cfg.KnownIPs == nil {
		s.cfg.KnownIPs = map[string]time.Time{}
	}
	s.audit = openAudit(dir)
	s.limiter = common.NewLimiter(5, 5*time.Minute, time.Minute)
	s.sessions = common.NewSessions("sd_srv", func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.cfg.PasswordHash
	})
	return s, s.save()
}

func (s *Server) cfgPath() string { return common.Join(s.dir, "server.json") }

// save must be called with s.mu held or before the server starts.
func (s *Server) save() error { return common.SaveJSON(s.cfgPath(), s.cfg) }

// SetPassword sets the emergency panel password (CLI "set-password"); ""
// removes it. Normal sign-in uses a Windows administrator account.
func (s *Server) SetPassword(pw string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pw == "" {
		s.cfg.PasswordHash = ""
	} else {
		s.cfg.PasswordHash = common.HashPassword(pw)
	}
	return s.save()
}

// CheckLogin is replaced in tests; on Windows it is winauth.CheckAdmin.
var CheckLogin = winauth.CheckAdmin

func (s *Server) ListenAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Listen
}

func (s *Server) WebAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Web
}

// Run serves agents, local ports and the admin page until ctx ends.
func (s *Server) Run(ctx context.Context) error {
	s.mu.Lock()
	wgMode := s.cfg.WGMode
	s.mu.Unlock()
	if wgMode != WGOnly {
		if err := s.listenAgents(s.ListenAddr()); err != nil {
			return fmt.Errorf("порт для точок %s: %w", s.ListenAddr(), err)
		}
		s.Log.Printf("сервер %s слухає точки на %s (TCP)", s.Version, s.ListenAddr())
	}
	s.startCrowdSec()
	if err := s.startWG(); err != nil {
		s.Log.Printf("ПОМИЛКА WireGuard на UDP %d: %v", s.ports().AgentPort, err)
	} else if wgMode != WGOff {
		s.Log.Printf("сервер %s слухає точки через WireGuard на UDP %d", s.Version, s.ports().AgentPort)
	}
	// The firewall rule is (re)applied at every start: the MSI cannot create
	// it, and this also repairs a rule someone deleted.
	s.applyFirewall()
	if p := s.ports(); s.FirewallAdd != nil && p.WebLAN {
		if err := s.FirewallAdd(fwPanel, "TCP", p.WebPort, nil); err != nil {
			s.Log.Printf("ПОМИЛКА брандмауера: %v", err)
		}
	}

	s.mu.Lock()
	s.ctx = ctx
	for key, port := range s.cfg.Ports {
		s.startPortLocked(key, port)
	}
	queued := map[string]bool{}
	for _, j := range s.jobs {
		queued[j.AgentID] = true
	}
	s.mu.Unlock()
	for id := range queued {
		s.wake(id) // expire stale jobs even if the point never returns
	}

	s.web = common.NewWebServer(s.routes())
	if err := s.web.Start(s.WebAddr()); err != nil {
		s.Log.Printf("ПОМИЛКА панелі керування на %s: %v", s.WebAddr(), err)
	} else {
		s.Log.Printf("панель керування: http://%s", s.WebAddr())
	}

	<-ctx.Done()
	s.web.Close()
	s.mu.Lock()
	if s.agentLn != nil {
		s.agentLn.Close()
	}
	if s.wgLn != nil {
		s.wgLn.Close()
	}
	if s.wg != nil {
		s.wg.Close()
	}
	for _, l := range s.listeners {
		l.Close()
	}
	for _, a := range s.online {
		a.sess.Close()
	}
	s.mu.Unlock()
	return nil
}

func (s *Server) tlsConfig() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12}
}

// listenAgents binds the port agents dial. On a move the new port is bound
// first and the old one closed after; connected points stay connected.
func (s *Server) listenAgents(addr string) error {
	ln, err := tls.Listen("tcp", addr, s.tlsConfig())
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := s.agentLn
	s.agentLn = ln
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				s.Log.Printf("accept: %v", err)
				time.Sleep(200 * time.Millisecond)
				continue
			}
			// Second layer behind the firewall rule: drop sources that are not
			// allowed before any TLS work is done.
			ip := remoteIP(c)
			if !s.sourceAllowed(ip) {
				s.noteRefused(ip, "адреса не в списку дозволених")
				c.Close()
				continue
			}
			// Limit connections that have not shown a code yet, per address
			// and in total, so nobody can tie up the server with open
			// handshakes. A banned address gets one at a time.
			banned := s.isBanned(ip)
			release, ok := s.enterHandshake(ip, banned)
			if !ok {
				c.Close()
				continue
			}
			go s.handleAgent(c, ip, banned, release, nil)
		}
	}()
	return nil
}

// Ports is what the "Налаштування" form edits.
type Ports struct {
	AgentPort int  `json:"agent_port"`
	WebLAN    bool `json:"web_lan"`
	WebPort   int  `json:"web_port"`
	FirstPort int  `json:"first_port"`
}

func (s *Server) ports() Ports {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ap, _ := net.SplitHostPort(s.cfg.Listen)
	agentPort, _ := strconv.Atoi(ap)
	lan, wp := common.ParsePanelAddr(s.cfg.Web)
	return Ports{AgentPort: agentPort, WebLAN: lan, WebPort: wp, FirstPort: s.cfg.FirstPort}
}

const (
	fwAgents = "SlipDrukka Server"
	fwPanel  = "SlipDrukka Server Panel"
)

// SetPorts moves listeners. Each change is applied only if the new port can
// be bound; what could not be applied keeps its old value and is reported.
func (s *Server) SetPorts(p Ports) error {
	for _, c := range []struct {
		v    int
		what string
	}{{p.AgentPort, "порт для точок"}, {p.WebPort, "порт панелі"}, {p.FirstPort, "перший порт принтерів"}} {
		if err := common.CheckPort(c.v, c.what); err != nil {
			return err
		}
	}
	if p.AgentPort == p.WebPort {
		return errors.New("порт для точок і порт панелі мають відрізнятися")
	}
	cur := s.ports()
	s.mu.Lock()
	for _, port := range s.cfg.Ports {
		if port == p.AgentPort || port == p.WebPort {
			s.mu.Unlock()
			return fmt.Errorf("порт %d уже зайнятий принтером точки", port)
		}
	}
	s.mu.Unlock()

	if p.AgentPort != cur.AgentPort {
		addr := ":" + strconv.Itoa(p.AgentPort)
		s.mu.Lock()
		only := s.cfg.WGMode == WGOnly
		s.mu.Unlock()
		if !only {
			if err := s.listenAgents(addr); err != nil {
				return fmt.Errorf("порт для точок %d не вдалося відкрити: %v — лишився %d", p.AgentPort, err, cur.AgentPort)
			}
		}
		s.mu.Lock()
		s.cfg.Listen = addr
		s.save()
		s.mu.Unlock()
		if err := s.startWG(); err != nil {
			s.Log.Printf("ПОМИЛКА WireGuard на UDP %d: %v", p.AgentPort, err)
		}
		s.applyFirewall()
		s.Log.Printf("порт для точок змінено: %d → %d (змініть перенаправлення на роутері)", cur.AgentPort, p.AgentPort)
	}

	if p.WebPort != cur.WebPort || p.WebLAN != cur.WebLAN {
		addr := common.PanelAddr(p.WebLAN, p.WebPort)
		if err := s.web.Start(addr); err != nil {
			return fmt.Errorf("панель на %s не вдалося відкрити: %v — лишилась стара адреса", addr, err)
		}
		s.mu.Lock()
		s.cfg.Web = addr
		s.save()
		s.mu.Unlock()
		if p.WebLAN && s.FirewallAdd != nil {
			if err := s.FirewallAdd(fwPanel, "TCP", p.WebPort, nil); err != nil {
				s.Log.Printf("ПОМИЛКА брандмауера: %v", err)
			}
		} else if !p.WebLAN && s.FirewallRemove != nil {
			s.FirewallRemove(fwPanel)
		}
		s.Log.Printf("панель керування тепер на %s", addr)
	}

	if p.FirstPort != cur.FirstPort {
		s.mu.Lock()
		s.cfg.FirstPort = p.FirstPort
		s.save()
		s.mu.Unlock()
		s.Log.Printf("перший порт для нових принтерів: %d", p.FirstPort)
	}
	return nil
}

func yamuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.KeepAliveInterval = 20 * time.Second
	c.ConnectionWriteTimeout = 30 * time.Second
	c.LogOutput = io.Discard
	return c
}

// handleAgent runs one point's connection. banned: the address is blocked;
// it is served only if it shows a valid code (a shop whose point knocked
// with an old code gets through at once after the admin gives it a new one),
// everything else is closed without a word and without a log line.
// release ends the handshake slot taken in the accept loop.
//
// peer: the point's WireGuard key when the connection came through the
// tunnel (nil for plain TCP). Such a point is already authenticated by
// WireGuard, so its failures are not counted towards a ban.
func (s *Server) handleAgent(c net.Conn, ip netip.Addr, banned bool, release func(), peer *wgtun.Key) {
	released := false
	finish := func() {
		if !released {
			released = true
			release()
		}
	}
	defer finish()
	remote := c.RemoteAddr().String()
	if banned {
		c.SetDeadline(time.Now().Add(5 * time.Second))
	} else {
		c.SetDeadline(time.Now().Add(20 * time.Second))
	}
	drop := func() { // banned address: silently
		s.noteBannedHit(ip)
		c.Close()
	}
	var hello proto.Hello
	if err := proto.ReadLine(c, &hello); err != nil {
		if banned {
			drop()
			return
		}
		c.Close()
		if peer != nil {
			return // a WireGuard check by the point itself, nothing to report
		}
		s.Log.Printf("точка %s: рукостискання не вдалося: %v", remote, err)
		s.noteFail(ip, "обірване або некоректне рукостискання", csHandshake)
		return
	}
	refuse := func(msg string) {
		proto.WriteLine(c, proto.Welcome{OK: false, Error: msg})
		s.Log.Printf("точка %s відхилена: %s", remote, msg)
		c.Close()
	}
	if hello.Proto != proto.Version {
		if banned {
			drop()
			return
		}
		// Not counted: an old program on a real point, not an attack.
		refuse(fmt.Sprintf("версія протоколу %d, сервер очікує %d — оновіть програму", hello.Proto, proto.Version))
		return
	}
	s.mu.Lock()
	var ac *AgentConfig
	usedPending := false // the point came with the key it was just issued
	h := common.SHA256Hex(hello.Key)
	for _, a := range s.cfg.Agents {
		if subtle.ConstantTimeCompare([]byte(a.KeyHash), []byte(h)) == 1 {
			ac = a
		}
		if a.PendingKey != "" && subtle.ConstantTimeCompare([]byte(common.SHA256Hex(a.PendingKey)), []byte(h)) == 1 {
			ac, usedPending = a, true
		}
	}
	expired := ac != nil && s.codeExpiredLocked(ac, time.Now())
	s.mu.Unlock()
	if ac == nil {
		if banned {
			drop()
			return
		}
		refuse("невідомий код підключення (точку видалили або код перевипущено)")
		if peer == nil {
			s.noteFail(ip, "невідомий код підключення", csUnknownCode)
		}
		return
	}
	if expired {
		if banned {
			drop()
			return
		}
		refuse("код підключення протермінований — видайте точці новий код у панелі сервера")
		if peer == nil {
			s.noteFail(ip, "протермінований код точки «"+ac.Name+"»", csExpiredCode)
		}
		return
	}
	wantWG := ac.WGPub
	if usedPending {
		wantWG = wgtun.PointPublic(hello.Key).String()
	}
	if peer != nil && wantWG != peer.String() {
		refuse("ключ WireGuard не відповідає коду цієї точки")
		return
	}
	if !s.agentSourceAllowed(ac, ip) {
		if banned {
			drop()
			return
		}
		s.noteRefused(ip, "точка «"+ac.Name+"»: код дозволено лише з інших адрес")
		refuse("код цієї точки не дозволено з адреси " + ip.String())
		return
	}

	printers := cleanPrinters(hello.Printers)

	s.mu.Lock()
	newKey, dirty := s.ownKeyLocked(ac, hello, usedPending)
	if s.learnWGKeyLocked(ac, hello.Key) {
		dirty = true
	}
	if dirty {
		if err := s.save(); err != nil {
			s.Log.Printf("ПОМИЛКА збереження конфігу: %v", err)
		}
	}
	wgPub := s.welcomeWGLocked()
	s.mu.Unlock()
	via := viaName(peer)
	if err := proto.WriteLine(c, proto.Welcome{OK: true, Name: ac.Name, WireGuard: wgPub, Via: via, Lang: s.logLang(), SeenAs: ip.String(), NewKey: newKey}); err != nil {
		c.Close()
		return
	}
	c.SetDeadline(time.Time{})
	finish()
	s.noteGood(ac.ID, ip)
	sess, err := yamux.Server(c, yamuxConfig())
	if err != nil {
		c.Close()
		return
	}
	conn := &agentConn{id: ac.ID, remote: remote, via: via, hostname: hello.Hostname, version: hello.Version,
		since: time.Now(), printers: printers, sess: sess, inv: hello.Inventory, invAt: time.Now(), seenAs: ip.String()}

	s.mu.Lock()
	if old := s.online[ac.ID]; old != nil {
		old.sess.Close()
	}
	s.online[ac.ID] = conn
	for _, p := range printers {
		s.ensurePortLocked(ac.ID, p.Alias)
	}
	s.mu.Unlock()
	if peer != nil {
		s.Log.Printf("точка «%s» підключилась з %s через WireGuard (%s, %d принтер(и))", ac.Name, remote, hello.Hostname, len(printers))
	} else {
		s.Log.Printf("точка «%s» підключилась з %s (%s, %d принтер(и))", ac.Name, remote, hello.Hostname, len(printers))
	}
	s.noteEvents(ac.ID, hello.Inventory)
	s.wake(ac.ID) // deliver whatever waited for this point
	go s.measureRTT(conn)

	// Streams the point opens: inventory pushes.
	go func() {
		for {
			st, err := sess.Accept()
			if err != nil {
				return
			}
			go s.handlePush(conn, st)
		}
	}()

	<-sess.CloseChan()

	s.mu.Lock()
	if s.online[ac.ID] == conn {
		delete(s.online, ac.ID)
	}
	s.mu.Unlock()
	s.Log.Printf("точка «%s» відключилась", ac.Name)
}

func portKey(agentID, alias string) string { return agentID + "/" + alias }

// MaxPrintersPerPoint limits how many printers one point may offer, and how
// many local ports it may hold in total. What a point reports is not
// trusted: every new name gets a listening port that is kept until the
// admin frees it, so without a limit a point whose code leaked could make
// the server open ports until none are left. No shop has that many printers.
var MaxPrintersPerPoint = 64

// cleanPrinters makes a list reported by a point safe: unique sanitised
// aliases, no empty names, at most MaxPrintersPerPoint entries.
func cleanPrinters(list []proto.Printer) []proto.Printer {
	seen := map[string]bool{}
	var out []proto.Printer
	for _, p := range list {
		p.Alias = proto.SanitizeName(p.Alias)
		if p.Name == "" || seen[p.Alias] {
			continue
		}
		if len(out) >= MaxPrintersPerPoint {
			break
		}
		seen[p.Alias] = true
		out = append(out, p)
	}
	return out
}

// ensurePortLocked gives (agent, alias) a stable local port, or 0 when the
// point already holds as many ports as it may or no port is free. s.mu held.
func (s *Server) ensurePortLocked(agentID, alias string) int {
	key := portKey(agentID, alias)
	if p, ok := s.cfg.Ports[key]; ok {
		if _, running := s.listeners[key]; !running {
			s.startPortLocked(key, p)
		}
		return p
	}
	refuse := func(why string) int {
		if !s.portless[key] {
			s.portless[key] = true
			s.Log.Printf("ПОМИЛКА: точка «%s»: принтер «%s» не отримав порт — %s", s.agentNameLocked(agentID), alias, why)
		}
		return 0
	}
	used := map[int]bool{}
	held := 0
	for k, p := range s.cfg.Ports {
		used[p] = true
		if strings.HasPrefix(k, agentID+"/") {
			held++
		}
	}
	if held >= MaxPrintersPerPoint {
		return refuse(fmt.Sprintf("за точкою вже закріплено %d портів; звільніть непотрібні («Звільнити порт»)", held))
	}
	port := s.cfg.FirstPort
	for used[port] || !portFree(port) {
		port++
		if port > 65535 {
			return refuse("вільних портів немає")
		}
	}
	delete(s.portless, key)
	s.cfg.Ports[key] = port
	if err := s.save(); err != nil {
		s.Log.Printf("ПОМИЛКА збереження конфігу: %v", err)
	}
	s.startPortLocked(key, port)
	return port
}

func portFree(port int) bool {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// startPortLocked opens 127.0.0.1:port for one remote printer. s.mu held.
func (s *Server) startPortLocked(key string, port int) {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		s.Log.Printf("ПОМИЛКА: порт 127.0.0.1:%d зайнятий: %v", port, err)
		return
	}
	s.listeners[key] = l
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			if s.LocalClient != nil {
				if err := s.LocalClient(c); err != nil {
					s.refuseLocal(c, port, err)
					continue
				}
			}
			// The order number is taken here, in the order connections
			// arrive, not later in the goroutine: two jobs sent one right
			// after another must never swap.
			go s.acceptJob(key, c, s.startIncoming(key))
		}
	}()
}

// refuseLocal drops a connection to a local printer port that did not come
// from the print spooler. Windows sees a reset; the log gets at most one
// line a minute.
func (s *Server) refuseLocal(c net.Conn, port int, why error) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetLinger(0)
	}
	c.Close()
	s.mu.Lock()
	quiet := time.Since(s.refusedAt) < time.Minute
	if !quiet {
		s.refusedAt = time.Now()
	}
	s.mu.Unlock()
	if !quiet {
		s.Log.Printf("відхилено з'єднання з локальним портом 127.0.0.1:%d: %v. Друкувати можна лише через принтер Windows", port, why)
	}
}

func (s *Server) statsLocked(key string) *printerStats {
	st := s.stats[key]
	if st == nil {
		st = &printerStats{}
		s.stats[key] = st
	}
	return st
}

// ---------------------------------------------------------------- admin ops

func (s *Server) findAgentLocked(id string) *AgentConfig {
	for _, a := range s.cfg.Agents {
		if a.ID == id {
			return a
		}
	}
	return nil
}

func (s *Server) codeLocked(key string) (string, error) {
	if s.cfg.PublicAddr == "" {
		return "", errors.New("спершу вкажіть зовнішню адресу сервера (налаштування)")
	}
	return proto.Code{Addr: s.cfg.PublicAddr, Key: key, Fingerprint: tlsutil.Fingerprint(s.cert), WireGuard: s.welcomeWGLocked()}.String(), nil
}

// AddAgent creates a point and returns its connection code.
func (s *Server) AddAgent(name string) (id, code string, err error) {
	name = proto.SanitizeName(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.cfg.Agents {
		if strings.EqualFold(a.Name, name) {
			return "", "", fmt.Errorf("точка «%s» вже є", name)
		}
	}
	key := common.RandomToken(24)
	code, err = s.codeLocked(key)
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	a := &AgentConfig{ID: common.RandomToken(6), Name: name, KeyHash: common.SHA256Hex(key), Created: now, CodeIssued: now}
	s.cfg.Agents = append(s.cfg.Agents, a)
	s.learnWGKeyLocked(a, key)
	s.Log.Printf("додано точку «%s»", name)
	return a.ID, code, s.save()
}

// Rekey issues a new code; the old one stops working at once.
func (s *Server) Rekey(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.findAgentLocked(id)
	if a == nil {
		return "", errors.New("точки немає")
	}
	key := common.RandomToken(24)
	code, err := s.codeLocked(key)
	if err != nil {
		return "", err
	}
	a.KeyHash = common.SHA256Hex(key)
	a.CodeIssued, a.CodeUsed = time.Now(), false
	a.OwnKey, a.PendingKey = false, ""
	s.learnWGKeyLocked(a, key)
	if c := s.online[id]; c != nil {
		c.sess.Close()
	}
	s.Log.Printf("точка «%s»: видано новий код, старий більше не діє", a.Name)
	return code, s.save()
}

func (s *Server) DeleteAgent(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.findAgentLocked(id)
	if a == nil {
		return errors.New("точки немає")
	}
	out := s.cfg.Agents[:0]
	for _, x := range s.cfg.Agents {
		if x.ID != id {
			out = append(out, x)
		}
	}
	s.cfg.Agents = out
	s.retireWGKeyLocked(a.WGPub)
	if err := s.updatePeersLocked(); err != nil {
		s.Log.Printf("ПОМИЛКА WireGuard: %v", err)
	}
	for key := range s.cfg.Ports {
		if strings.HasPrefix(key, id+"/") {
			if l := s.listeners[key]; l != nil {
				l.Close()
				delete(s.listeners, key)
			}
			delete(s.cfg.Ports, key)
		}
	}
	if c := s.online[id]; c != nil {
		c.sess.Close()
	}
	for jid, j := range s.jobs {
		if j.AgentID == id {
			delete(s.jobs, jid)
			os.Remove(s.jobData(jid))
			os.Remove(s.jobMeta(jid))
		}
	}
	s.Log.Printf("точку «%s» видалено", a.Name)
	return s.save()
}

func (s *Server) SetPublicAddr(addr string) error {
	addr = strings.TrimSpace(addr)
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return errors.New("потрібно у вигляді адреса:порт, наприклад 203.0.113.10:7310")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.PublicAddr = addr
	return s.save()
}

// ---------------------------------------------------------------- state for UI

type PrinterView struct {
	Alias   string  `json:"alias"`
	Name    string  `json:"name"`
	Port    int     `json:"port"`
	Offered bool    `json:"offered"` // the point currently offers it
	Mode    string  `json:"mode"`    // proto.ModeRaw or proto.ModeXPS
	Queues  []Queue `json:"queues"`  // Windows queues on this server using the port
	// The printer at the point, from the point's last report.
	ShopDriver        string    `json:"shop_driver"`
	ShopDriverVersion int       `json:"shop_driver_version"`
	ShopPort          string    `json:"shop_port"`
	ShopPortHost      string    `json:"shop_port_host"`
	ShopPortNumber    int       `json:"shop_port_number"`
	ShopMissing       bool      `json:"shop_missing"` // offered but not in Windows at the point
	OK                int       `json:"ok"`
	Failed            int       `json:"failed"`
	Queued            int       `json:"queued"`
	LastJob           time.Time `json:"last_job"`
	LastError         string    `json:"last_error"`
}

type AgentView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Online     bool      `json:"online"`
	Remote     string    `json:"remote"`
	Via        string    `json:"via"`
	HasWG      bool      `json:"has_wg"` // the server knows the point's WireGuard key
	Hostname   string    `json:"hostname"`
	Version    string    `json:"version"`
	Since      time.Time `json:"since"`
	AllowedIPs []string  `json:"allowed_ips"`
	Seen       []string  `json:"seen"`
	// CodeExpires: when the unused code stops working (zero: it does not).
	CodeExpires time.Time     `json:"code_expires"`
	CodeExpired bool          `json:"code_expired"`
	Printers    []PrinterView `json:"printers"`
	Jobs        []JobView     `json:"jobs"`
	// Inventory: everything the point reported (all its Windows printers,
	// USB ports) and its events, for the "add printer" dialog and warnings.
	Inventory   *proto.Inventory `json:"inventory,omitempty"`
	InventoryAt time.Time        `json:"inventory_at"`
	Events      []proto.Event    `json:"events"`
	// SeenAs: the point's public address; RTT: round trips over its
	// connection in the last hour (0.17.0).
	SeenAs string      `json:"seen_as,omitempty"`
	RTT    []RTTSample `json:"rtt"`
	// OwnKey: the point has its own key; its connection code no longer works.
	OwnKey bool `json:"own_key"`
}

type StateView struct {
	Version     string        `json:"version"`
	Listen      string        `json:"listen"`
	PublicAddr  string        `json:"public_addr"`
	Fingerprint string        `json:"fingerprint"`
	Agents      []AgentView   `json:"agents"`
	Log         []string      `json:"log"`
	QueueError  string        `json:"queue_error,omitempty"`
	JobTTL      int           `json:"job_ttl_minutes"`
	Ports       Ports         `json:"ports"`
	Allowed     []string      `json:"allowed"`
	OnlineFrom  []string      `json:"online_from"`
	Refused     []RefusedView `json:"refused"`
	Security    SecurityView  `json:"security"`
	Bans        []BanView     `json:"bans"`
	Alerts      []*Alert      `json:"alerts"`
	Audit       []AuditEntry  `json:"audit"`
	CrowdSec    CrowdSecView  `json:"crowdsec"`
	LogLang     string        `json:"log_lang"`
	// Emergency: an emergency password is set; EmergencyLAN: it is accepted
	// from the network.
	Emergency    bool `json:"emergency"`
	EmergencyLAN bool `json:"emergency_lan"`
	// Warning is a problem of the service itself, not of a shop (it runs
	// with LocalSystem rights although it should not).
	Warning string `json:"warning,omitempty"`
}

// queues asks Windows which queues use our ports. PowerShell is slow, so the
// answer is cached for a few seconds; CreateQueue drops the cache.
func (s *Server) queues() (map[string][]Queue, error) {
	s.qMu.Lock()
	defer s.qMu.Unlock()
	if s.qCache != nil && time.Since(s.qAt) < 15*time.Second {
		return s.qCache, nil
	}
	q, err := s.admin.QueuesByPort()
	if err != nil {
		return nil, err
	}
	s.qCache, s.qAt = q, time.Now()
	return q, nil
}

// SetWarning sets (or, with "", clears) the warning shown at the top of the
// admin page.
func (s *Server) SetWarning(text string) {
	s.mu.Lock()
	s.warning = text
	s.mu.Unlock()
}

func (s *Server) State() StateView {
	cs := s.crowdSecView()
	queues, qerr := s.queues()
	ports := s.ports()
	onlineFrom := s.OnlineSources()
	s.mu.Lock()
	defer s.mu.Unlock()
	v := StateView{Version: s.Version, Listen: s.cfg.Listen, PublicAddr: s.cfg.PublicAddr,
		Fingerprint: tlsutil.Fingerprint(s.cert), Log: s.Log.Lines(), JobTTL: int(s.ttl() / time.Minute), Ports: ports,
		Allowed: s.cfg.Allowed, Refused: s.refusedViewsLocked(), OnlineFrom: onlineFrom,
		Security: s.securityLocked(), Bans: s.bansLocked(time.Now()), Alerts: append([]*Alert(nil), s.cfg.Alerts...),
		Audit: s.audit.recent(100), CrowdSec: cs, LogLang: s.cfg.Lang,
		Emergency: s.cfg.PasswordHash != "", EmergencyLAN: s.cfg.EmergencyLAN, Warning: s.warning}
	if qerr != nil {
		v.QueueError = qerr.Error()
	}
	for _, a := range s.cfg.Agents {
		av := AgentView{ID: a.ID, Name: a.Name, AllowedIPs: a.AllowedIPs, Seen: a.Seen, CodeExpires: s.codeExpiresLocked(a), HasWG: a.WGPub != "", OwnKey: a.OwnKey}
		av.CodeExpired = !av.CodeExpires.IsZero() && time.Now().After(av.CodeExpires)
		conn := s.online[a.ID]
		offered := map[string]string{}
		modes := map[string]string{}
		if conn != nil {
			av.Online, av.Remote, av.Hostname, av.Version, av.Since = true, conn.remote, conn.hostname, conn.version, conn.since
			av.Via = conn.via
			av.SeenAs = conn.seenAs
			av.RTT = append([]RTTSample(nil), conn.rtt...)
			av.Inventory, av.InventoryAt = conn.inv, conn.invAt
			for _, p := range conn.printers {
				offered[p.Alias] = p.Name
				modes[p.Alias] = p.Mode
			}
		}
		for key, port := range s.cfg.Ports {
			id, alias, _ := strings.Cut(key, "/")
			if id != a.ID {
				continue
			}
			name, isOffered := offered[alias]
			pv := PrinterView{Alias: alias, Name: name, Port: port, Offered: isOffered, Mode: modes[alias], Queues: queues[PortName(port)]}
			if conn != nil && conn.inv != nil && isOffered {
				pv.ShopMissing = true
				for _, ip := range conn.inv.Printers {
					if ip.Name == name {
						pv.ShopMissing = false
						pv.ShopDriver, pv.ShopDriverVersion, pv.ShopPort = ip.Driver, ip.DriverVersion, ip.Port
						pv.ShopPortHost, pv.ShopPortNumber = ip.PortHost, ip.PortNumber
					}
				}
			}
			if st := s.stats[key]; st != nil {
				pv.OK, pv.Failed, pv.LastJob, pv.LastError = st.OK, st.Failed, st.LastJob, st.LastError
			}
			av.Printers = append(av.Printers, pv)
		}
		av.Jobs = s.jobViewsLocked(a.ID)
		av.Events = append([]proto.Event(nil), s.events[a.ID]...)
		for i := range av.Printers {
			for _, j := range av.Jobs {
				if j.Alias == av.Printers[i].Alias {
					av.Printers[i].Queued++
				}
			}
		}
		sort.Slice(av.Printers, func(i, j int) bool { return av.Printers[i].Port < av.Printers[j].Port })
		v.Agents = append(v.Agents, av)
	}
	sort.Slice(v.Agents, func(i, j int) bool { return v.Agents[i].Name < v.Agents[j].Name })
	return v
}

// PortName is the Windows printer port name used for a local RAW port.
func PortName(port int) string { return "SLIPDRUKKA_" + strconv.Itoa(port) }

// CreateQueue makes a Windows printer on this server for one remote printer.
func (s *Server) CreateQueue(agentID, alias, driver, queue string) error {
	s.mu.Lock()
	port, ok := s.cfg.Ports[portKey(agentID, alias)]
	s.mu.Unlock()
	if !ok {
		return errors.New("такого принтера точки немає")
	}
	queue = strings.TrimSpace(queue)
	if queue == "" || driver == "" {
		return errors.New("потрібні назва принтера і драйвер")
	}
	if err := s.admin.CreateQueue(queue, driver, PortName(port), port); err != nil {
		return err
	}
	s.qMu.Lock()
	s.qCache = nil
	s.qMu.Unlock()
	s.Log.Printf("створено принтер Windows «%s» (драйвер «%s», порт 127.0.0.1:%d)", queue, driver, port)
	return nil
}

// logLang is the language of the log files. Read without s.mu: the log is
// written while s.mu is held.
func (s *Server) logLang() string {
	if v, ok := s.lang.Load().(string); ok {
		return v
	}
	return i18n.UK
}

// SetLang changes the language of the log files (here and, from their next
// connection, at the points).
func (s *Server) SetLang(lang string) error {
	if lang != i18n.UK && lang != i18n.EN {
		return errors.New("невідома мова")
	}
	s.mu.Lock()
	s.cfg.Lang = lang
	err := s.save()
	s.mu.Unlock()
	s.lang.Store(lang)
	return err
}

// SetEmergencyLAN allows or forbids the emergency password from other
// computers.
func (s *Server) SetEmergencyLAN(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.EmergencyLAN = on
	return s.save()
}

func (s *Server) emergencyLAN() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.EmergencyLAN
}
