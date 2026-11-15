package agent

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/server"
	"slipdrukka/internal/winprint"
)

type wgNoAdmin struct{}

func (wgNoAdmin) Drivers() ([]string, error) { return nil, nil }
func (wgNoAdmin) QueuesByPort() (map[string][]server.Queue, error) {
	return map[string][]server.Queue{}, nil
}
func (wgNoAdmin) CreateQueue(string, string, string, int) error { return nil }
func (wgNoAdmin) RenameQueue(string, string) error              { return nil }
func (wgNoAdmin) QueuePaper(string) (winprint.Paper, error)     { return winprint.Paper{}, nil }
func (wgNoAdmin) SetQueuePaper(string, int, int) (winprint.Paper, error) {
	return winprint.Paper{}, nil
}

// a port free for both TCP and UDP
func freeBoth(t *testing.T) int {
	for i := 0; i < 50; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		u, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
		l.Close()
		if err == nil {
			u.Close()
			return port
		}
	}
	t.Fatal("немає вільного порту")
	return 0
}

func freeTCP(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func wait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("не дочекались: " + what)
}

type wgSetup struct {
	srv  *server.Server
	port int
	ctx  context.Context
}

func startServer(t *testing.T, mode string) *wgSetup {
	// Under the race detector the userspace network stack inside WireGuard
	// (gVisor's sleeper) spins instead of sleeping: measured 745% CPU in
	// sleep.(*Sleeper).nextWaker against 3% without -race, and the tests
	// time out at random. The tunnel itself is covered without -race and by
	// the wgtun package; everything else here runs under -race as usual.
	if raceOn && mode != server.WGOff {
		t.Skip("тести з тунелем WireGuard не запускаються під -race (див. коментар)")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	port := freeBoth(t)
	dir := t.TempDir()
	common.SaveJSON(dir+"/server.json", map[string]any{"listen": "127.0.0.1:" + strconv.Itoa(port),
		"web": "127.0.0.1:" + strconv.Itoa(freeTCP(t)), "first_port": freeTCP(t), "wg_mode": mode})
	srv, err := server.New(dir, common.NewLogRing("", 100), wgNoAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetPublicAddr("127.0.0.1:" + strconv.Itoa(port))
	go srv.Run(ctx)
	// wait until the server really listens (not a fixed sleep: under -race
	// the start can take longer, and a test that changes the mode too early
	// would race with it)
	if mode != server.WGOnly {
		wait(t, "сервер слухає", func() bool {
			c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
			if err == nil {
				c.Close()
			}
			return err == nil
		})
	}
	time.Sleep(100 * time.Millisecond) // WireGuard starts right after TCP
	return &wgSetup{srv: srv, port: port, ctx: ctx}
}

func startPoint(t *testing.T, ctx context.Context, code string) *Agent {
	dir := t.TempDir()
	common.SaveJSON(dir+"/agent.json", map[string]any{"web": "127.0.0.1:" + strconv.Itoa(freeTCP(t))})
	ag, err := New(dir, common.NewLogRing("", 100), &winprint.Fake{Printers: []winprint.PrinterInfo{{Name: "P"}}})
	if err != nil {
		t.Fatal(err)
	}
	ag.SetShared([]proto.Printer{{Name: "P", Alias: "p"}})
	ag.SetCode(code)
	go ag.Run(ctx)
	return ag
}

func via(s *server.Server) string {
	st := s.State()
	if len(st.Agents) == 0 || !st.Agents[0].Online {
		return ""
	}
	return st.Agents[0].Via
}

// A code from this version carries the server's WireGuard key: the very
// first connection goes through the tunnel.
func TestWGFirstConnect(t *testing.T) {
	w := startServer(t, server.WGOn)
	_, code, err := w.srv.AddAgent("shop")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := proto.ParseCode(code); c.WireGuard == "" {
		t.Fatal("у коді немає ключа WireGuard")
	}
	ag := startPoint(t, w.ctx, code)
	wait(t, "підключення через WireGuard", func() bool { return via(w.srv) == proto.ViaWireGuard })
	wait(t, "точка теж бачить WireGuard", func() bool { return ag.Status().Via == proto.ViaWireGuard })
}

// UDP does not get through (the server's WireGuard is off while the point
// still has the key): after wgTimeout the point connects over TCP and
// forgets the key the server no longer offers.
func TestWGFallbackToTCP(t *testing.T) {
	old := wgTimeout
	wgTimeout = 2 * time.Second
	defer func() { wgTimeout = old }()
	w := startServer(t, server.WGOn)
	_, code, _ := w.srv.AddAgent("shop")
	if err := w.srv.SetWGMode(server.WGOff); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ag := startPoint(t, w.ctx, code)
	wait(t, "підключення через TCP", func() bool { return via(w.srv) == proto.ViaTCP })
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("перехід на TCP зайняв %v", d)
	}
	wait(t, "точка забула ключ WireGuard, якого сервер більше не пропонує", func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return ag.cfg.ServerWG == ""
	})
}

// A point with an old code (no WireGuard key in it) connects over TCP,
// learns the key from the greeting and moves to WireGuard when it checks
// again; "only" mode then works with the TCP port closed.
func TestWGUpgradeAndOnly(t *testing.T) {
	oldRetry, oldFirst := wgRetry, wgFirstRetry
	wgRetry, wgFirstRetry = time.Second, time.Second
	defer func() { wgRetry, wgFirstRetry = oldRetry, oldFirst }()
	w := startServer(t, server.WGOn)
	_, code, _ := w.srv.AddAgent("shop")
	c, _ := proto.ParseCode(code)
	c.WireGuard = "" // as issued by 0.9
	startPoint(t, w.ctx, c.String())
	wait(t, "перехід на WireGuard", func() bool { return via(w.srv) == proto.ViaWireGuard })

	if err := w.srv.SetWGMode(server.WGOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(w.port), time.Second); err == nil {
		t.Fatal("у режимі «лише WireGuard» порт TCP мав закритися")
	}
	if via(w.srv) != proto.ViaWireGuard {
		t.Fatal("точка через WireGuard мала лишитися на зв'язку")
	}
}

// "Only" mode is refused while some point's WireGuard key is unknown.
func TestWGOnlyNeedsKeys(t *testing.T) {
	w := startServer(t, server.WGOff)
	_, code, _ := w.srv.AddAgent("shop")
	_ = code
	// a point from before 0.10: the server never saw its secret
	st := w.srv.State()
	if len(st.Security.WGMissing) != 0 {
		t.Fatalf("новий код мав одразу дати ключ: %v", st.Security.WGMissing)
	}
}

// While on plain TLS because WireGuard failed earlier, the point checks
// again every wgRetry and moves to WireGuard on its own.
func TestWGWatchMovesOver(t *testing.T) {
	oldRetry, oldFirst := wgRetry, wgFirstRetry
	wgRetry, wgFirstRetry = 2*time.Second, 2*time.Second
	defer func() { wgRetry, wgFirstRetry = oldRetry, oldFirst }()
	w := startServer(t, server.WGOn)
	_, code, _ := w.srv.AddAgent("shop")
	dir := t.TempDir()
	common.SaveJSON(dir+"/agent.json", map[string]any{"web": "127.0.0.1:" + strconv.Itoa(freeTCP(t))})
	ag, err := New(dir, common.NewLogRing("", 100), &winprint.Fake{Printers: []winprint.PrinterInfo{{Name: "P"}}})
	if err != nil {
		t.Fatal(err)
	}
	ag.SetCode(code)
	ag.mu.Lock()
	ag.wgFailedAt = time.Now().Add(time.Hour) // as if WireGuard had just failed
	ag.mu.Unlock()
	go ag.Run(w.ctx)
	wait(t, "спершу TCP", func() bool { return via(w.srv) == proto.ViaTCP })
	wait(t, "сам перейшов на WireGuard", func() bool { return via(w.srv) == proto.ViaWireGuard })
}

// After the first WireGuard failure the point tries again a minute later;
// after more failures in a row only every wgRetry.
func TestWGRetrySchedule(t *testing.T) {
	a := &Agent{}
	a.wgFails = 1
	if d := a.wgDelayLocked(); d != wgFirstRetry {
		t.Fatalf("після першого збою %v, мало бути %v", d, wgFirstRetry)
	}
	a.wgFails = 2
	if d := a.wgDelayLocked(); d != wgRetry {
		t.Fatalf("після кількох збоїв %v, мало бути %v", d, wgRetry)
	}
}
