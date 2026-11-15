// Package agent is the part that runs on the shop PC. It dials the server
// (so the shop router needs no port forwarding), offers the printers the
// admin ticked, and prints incoming RAW jobs through the Windows spooler.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"

	"slipdrukka/internal/common"
	"slipdrukka/internal/i18n"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/wgtun"
	"slipdrukka/internal/winprint"
)

type Config struct {
	Code string `json:"code"`
	// ServerWG: the server's WireGuard key (from the code or the server's
	// greeting); empty = connect over plain TLS only.
	ServerWG string `json:"server_wg,omitempty"`
	// Lang: the language of the log file, as the server says (uk/en).
	Lang   string          `json:"lang,omitempty"`
	Shared []proto.Printer `json:"shared"`
	Web    string          `json:"web"`
	// Track remembers port and driver of each shared printer (by alias), so
	// a printer Windows renamed or re-drivered can be recognised.
	Track map[string]Track `json:"track,omitempty"`
	// Created lists the Windows printers this program created on a command
	// from the server. Together with Shared they are the only printers the
	// server may rename or re-point (see ours in commands.go).
	Created []string `json:"created,omitempty"`
}

type Track struct {
	Port   string `json:"port"`
	Driver string `json:"driver"`
}

type Status struct {
	Connected bool      `json:"connected"`
	Name      string    `json:"name"`   // point name given by the server
	Server    string    `json:"server"` // address from the code
	Error     string    `json:"error"`
	Since     time.Time `json:"since"`
	Version   string    `json:"version"`
	Via       string    `json:"via"` // proto.ViaWireGuard or proto.ViaTCP
	// 0.17.0: this point's public (NAT) address as the server sees it, the
	// address the server's name led to, and the last round trip to it.
	SeenAs   string  `json:"seen_as,omitempty"`
	ServerIP string  `json:"server_ip,omitempty"`
	RTTms    float64 `json:"rtt_ms,omitempty"`
}

type Agent struct {
	Version string
	dir     string
	Log     *common.LogRing
	spool   winprint.Spooler

	mu            sync.Mutex
	warning       string // shown in the page (SetWarning)
	netMu         sync.Mutex
	netCache      *proto.NetInfo
	netAt         time.Time
	cfg           Config
	status        Status
	kick          chan struct{}
	jobs          struct{ OK, Failed int }
	done          map[string]time.Time // recently printed job IDs (duplicate guard)
	web           *common.WebServer
	sess          *yamux.Session  // current server session (for pushes)
	refused       bool            // the server refused the current code
	events        []proto.Event   // recent events, newest last
	missing       map[string]bool // aliases already reported missing
	fallbackNoted map[string]bool // alias|reason already reported (fallback.go)
	dirty         chan struct{}   // ask the watcher to re-check and push
	invHidden     int             // printers left out of the last report (fitInventory)
	ports         portCache
	wgFailedAt    time.Time    // WireGuard did not work: plain TLS until wgRetry passes
	wgFails       int          // WireGuard failures in a row (wgDelayLocked)
	busy          atomic.Int32 // print jobs and commands in progress
	wgMu          sync.Mutex
	wgc           *wgtun.Client // the point's WireGuard tunnel, kept between connections
	lang          atomic.Value  // log language, read by the log without a.mu

	// Hooks set by main on Windows; nil in tests.
	Admin   Admin                  // creates printers in Windows
	OnPanel func(url string) error // tells the tray icon where the page is
}

// panelPort is the port of the point's own page.
func (a *Agent) panelPort() int {
	_, port := common.ParsePanelAddr(a.WebAddr())
	return port
}

func (a *Agent) publishPanel() {
	if a.OnPanel == nil {
		return
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", a.panelPort())
	if err := a.OnPanel(url); err != nil {
		a.Log.Printf("не вдалося записати адресу панелі для значка: %v", err)
	}
}

func New(dir string, log *common.LogRing, spool winprint.Spooler) (*Agent, error) {
	a := &Agent{dir: dir, Log: log, spool: spool, kick: make(chan struct{}, 1), done: map[string]time.Time{},
		missing: map[string]bool{}, fallbackNoted: map[string]bool{}, dirty: make(chan struct{}, 1)}
	_ = common.LoadJSON(common.Join(dir, "done.json"), &a.done)
	_, statErr := os.Stat(a.cfgPath())
	defer func() {
		if a.cfg.Lang == "" {
			if os.IsNotExist(statErr) {
				a.cfg.Lang = i18n.System()
			} else {
				a.cfg.Lang = i18n.UK
			}
		}
		a.lang.Store(a.cfg.Lang)
		log.Lang = func() string { // without a.mu: the log is written while it is held
			if v, ok := a.lang.Load().(string); ok {
				return v
			}
			return i18n.UK
		}
	}()
	if err := common.LoadJSON(a.cfgPath(), &a.cfg); err != nil {
		return nil, fmt.Errorf("конфіг: %w", err)
	}
	// The point's page has no password, so it is only ever served to this
	// computer itself, whatever an old settings file says.
	_, port := common.ParsePanelAddr(a.cfg.Web)
	if port == 0 {
		port = 7320
	}
	a.cfg.Web = common.PanelAddr(false, port)
	if a.cfg.Track == nil {
		a.cfg.Track = map[string]Track{}
	}
	return a, a.saveLocked()
}

func (a *Agent) cfgPath() string   { return common.Join(a.dir, "agent.json") }
func (a *Agent) saveLocked() error { return common.SaveJSON(a.cfgPath(), a.cfg) }

func (a *Agent) WebAddr() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Web
}

// CodeReplaceable: no code yet, or the server refused the current one
// (point deleted / code re-issued). Only then may the code be entered on
// the point's own page, which has no password.
func (a *Agent) CodeReplaceable() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Code == "" || (a.refused && !a.status.Connected)
}

// SetCode stores a new connection code ("" disconnects) and reconnects.
func (a *Agent) SetCode(code string) error {
	if code != "" {
		if _, err := proto.ParseCode(code); err != nil {
			return err
		}
	}
	wg := ""
	if c, err := proto.ParseCode(code); err == nil {
		wg = c.WireGuard
	}
	// Close the old tunnel right now, before the reconnect: closed later, it
	// could hit the tunnel the new connection has just built.
	a.dropWG()
	a.mu.Lock()
	a.cfg.Code = code
	a.cfg.ServerWG = wg
	a.wgFailedAt = time.Time{}
	a.refused = false
	err := a.saveLocked()
	a.mu.Unlock()
	a.reconnect()
	return err
}

// SetShared stores which printers are offered and tells the server.
func (a *Agent) SetShared(list []proto.Printer) error {
	seen := map[string]bool{}
	var clean []proto.Printer
	for _, p := range list {
		if p.Name == "" {
			continue
		}
		p.Alias = proto.SanitizeName(p.Alias)
		if p.Alias == "printer" && p.Name != "" {
			p.Alias = proto.SanitizeName(p.Name)
		}
		if p.Mode != proto.ModeRaw && p.Mode != proto.ModeXPS {
			return fmt.Errorf("невідомий режим «%s»", p.Mode)
		}
		if seen[p.Alias] {
			return fmt.Errorf("коротка назва «%s» повторюється", p.Alias)
		}
		seen[p.Alias] = true
		clean = append(clean, p)
	}
	a.mu.Lock()
	a.cfg.Shared = clean
	// Forget what we knew about printers no longer offered: a printer shared
	// later under the same alias must not look like a "driver change".
	for alias := range a.cfg.Track {
		if !seen[alias] {
			delete(a.cfg.Track, alias)
			delete(a.missing, alias)
		}
	}
	err := a.saveLocked()
	a.mu.Unlock()
	a.changed()
	return err
}

// changed asks the watcher to re-check printers and push the inventory.
func (a *Agent) changed() {
	select {
	case a.dirty <- struct{}{}:
	default:
	}
}

func (a *Agent) reconnect() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

func (a *Agent) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.status
	s.Version = a.Version
	return s
}

func (a *Agent) setStatus(f func(*Status)) {
	a.mu.Lock()
	f(&a.status)
	a.mu.Unlock()
}

// Run serves the local page and keeps the server connection up until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	a.web = common.NewWebServer(a.routes())
	if err := a.web.Start(a.WebAddr()); err != nil {
		a.Log.Printf("ПОМИЛКА панелі на %s: %v", a.WebAddr(), err)
	} else {
		a.Log.Printf("агент %s запущено, панель: http://%s", a.Version, a.WebAddr())
		a.publishPanel()
	}
	go func() { <-ctx.Done(); a.web.Close() }()
	go a.watch(ctx)
	// A code set before the start already counts: drop the reconnect request
	// it left, or the first session would be torn down right after it opens.
	select {
	case <-a.kick:
	default:
	}

	backoff := 2 * time.Second
	for ctx.Err() == nil {
		a.mu.Lock()
		code := a.cfg.Code
		a.mu.Unlock()
		if code == "" {
			a.setStatus(func(s *Status) {
				*s = Status{Error: "немає коду підключення — вставте код із панелі сервера"}
			})
			select {
			case <-ctx.Done():
			case <-a.kick:
			}
			continue
		}
		started := time.Now()
		err := a.session(ctx, code)
		if ctx.Err() != nil {
			break
		}
		if errors.Is(err, errKicked) {
			a.setStatus(func(s *Status) { s.Connected = false })
			backoff = 2 * time.Second
			continue
		}
		a.setStatus(func(s *Status) { s.Connected = false; s.Error = errString(err) })
		if err != nil {
			a.Log.Printf("зв'язок з сервером: %v", err)
		}
		var welcomeErr *refusedError
		refused := errors.As(err, &welcomeErr)
		a.mu.Lock()
		a.refused = refused
		a.mu.Unlock()
		if refused {
			backoff = time.Minute // wrong/revoked code: don't hammer the server
		} else if time.Since(started) > time.Minute {
			backoff = 2 * time.Second
		}
		select {
		case <-ctx.Done():
		case <-a.kick:
			backoff = 2 * time.Second
		case <-time.After(backoff):
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type refusedError struct{ msg string }

func (e *refusedError) Error() string { return "сервер відмовив: " + e.msg }

// session runs one connection to the server until it breaks or a kick comes.
func (a *Agent) session(ctx context.Context, codeText string) error {
	code, err := proto.ParseCode(codeText)
	if err != nil {
		return err
	}
	a.setStatus(func(s *Status) { s.Server = code.Addr })
	// The report is put together before dialling: it asks Windows about
	// printer ports through PowerShell, which can take many seconds right
	// after a start (measured: over 20 s on the first start under a new
	// service account), and the server waits only 20 s for the greeting.
	host, _ := os.Hostname()
	inv := a.inventory()
	conn, via, err := a.dial(ctx, code)
	if err != nil {
		return err
	}

	conn.SetDeadline(time.Now().Add(20 * time.Second))
	if err := proto.WriteLine(conn, proto.Hello{Proto: proto.Version, Version: a.Version, Key: code.Key, Hostname: host,
		Printers: inv.Shared, Inventory: inv, Caps: []string{proto.CapOwnKey}}); err != nil {
		conn.Close()
		return err
	}
	var w proto.Welcome
	if err := proto.ReadLine(conn, &w); err != nil {
		conn.Close()
		return fmt.Errorf("сервер не відповів: %w", err)
	}
	if !w.OK {
		conn.Close()
		return &refusedError{w.Error}
	}
	conn.SetDeadline(time.Time{})
	if w.NewKey != "" {
		// One-time code: the server gave this point its own key. Save it and
		// come back with it; the code stops working once the server sees it.
		conn.Close()
		return a.adoptKey(code, w.NewKey)
	}
	a.mu.Lock()
	if w.Lang != "" && w.Lang != a.cfg.Lang {
		a.cfg.Lang = w.Lang // the server's choice
		a.lang.Store(w.Lang)
		if err := a.saveLocked(); err != nil {
			a.Log.Printf("ПОМИЛКА збереження налаштувань: %v", err)
		}
	}
	if a.cfg.ServerWG != w.WireGuard {
		a.cfg.ServerWG = w.WireGuard // the server switched WireGuard on or off
		if w.WireGuard == "" {
			go a.dropWG()
		}
		if err := a.saveLocked(); err != nil {
			a.Log.Printf("ПОМИЛКА збереження налаштувань: %v", err)
		}
	}
	a.mu.Unlock()

	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = 20 * time.Second
	cfg.ConnectionWriteTimeout = 30 * time.Second
	cfg.LogOutput = io.Discard
	sess, err := yamux.Client(conn, cfg)
	if err != nil {
		conn.Close()
		return err
	}
	defer sess.Close()
	a.mu.Lock()
	a.sess = sess
	a.refused = false
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.sess == sess {
			a.sess = nil
		}
		a.mu.Unlock()
	}()
	serverIP := remoteHost(conn, via, code.Addr)
	a.setStatus(func(s *Status) {
		s.Connected, s.Name, s.Error, s.Since, s.Via = true, w.Name, "", time.Now(), via
		s.SeenAs, s.ServerIP, s.RTTms = w.SeenAs, serverIP, 0
	})
	go a.measureRTT(sess)
	if via == proto.ViaWireGuard {
		a.Log.Printf("підключено до сервера %s через WireGuard як «%s», видано принтерів: %d", code.Addr, w.Name, len(inv.Shared))
	} else {
		a.Log.Printf("підключено до сервера %s як «%s», видано принтерів: %d", code.Addr, w.Name, len(inv.Shared))
	}

	kicked := make(chan struct{})
	upgrade := make(chan struct{}, 1)
	if via == proto.ViaTCP && w.WireGuard != "" {
		go a.watchWG(sess, code, upgrade)
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-a.kick:
			a.Log.Printf("налаштування змінено — перепідключаюсь")
			close(kicked)
		case <-upgrade:
			a.Log.Printf("WireGuard знову доступний — перепідключаюсь через нього")
			close(kicked)
		case <-sess.CloseChan():
			return
		}
		sess.Close()
	}()

	for {
		st, err := sess.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			select {
			case <-kicked:
				return errKicked
			default:
			}
			return errors.New("з'єднання з сервером розірвано")
		}
		go a.handleJob(st)
	}
}

var errKicked = errors.New("перепідключення після зміни налаштувань")
