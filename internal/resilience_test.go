package internal_test

// Resilience: what happens to print jobs when the line between the server
// and a point misbehaves. The point reaches the server through a small TCP
// proxy that can cut connections, cut them after a number of bytes, swallow
// one direction or freeze. WireGuard is off here: the proxy speaks TCP.

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"slipdrukka/internal/agent"
	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/server"
	"slipdrukka/internal/winprint"
)

// flaky is the proxy between a point and the server.
type flaky struct {
	ln     net.Listener
	target string

	mu    sync.Mutex
	conns map[net.Conn]bool

	cutDownAfter atomic.Int64 // >0: cut everything once this many bytes went server→point
	down         atomic.Int64 // bytes server→point since the last arm
	dropUp       atomic.Bool  // swallow point→server (the point's confirmations are lost)
	dropUpAfter  atomic.Int64 // >0: start swallowing once this many bytes went server→point
	frozen       atomic.Bool  // forward nothing, close nothing
	refuse       atomic.Bool  // accept and close at once (line down)
}

func newFlaky(t *testing.T, target string) *flaky {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &flaky{ln: ln, target: target, conns: map[net.Conn]bool{}}
	t.Cleanup(func() { ln.Close(); p.cut() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if p.refuse.Load() {
				c.Close()
				continue
			}
			u, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			p.mu.Lock()
			p.conns[c], p.conns[u] = true, true
			p.mu.Unlock()
			go p.pipe(u, c, false) // point → server
			go p.pipe(c, u, true)  // server → point
		}
	}()
	return p
}

func (p *flaky) addr() string { return p.ln.Addr().String() }

func (p *flaky) pipe(dst, src net.Conn, down bool) {
	buf := make([]byte, 16*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			for p.frozen.Load() {
				time.Sleep(20 * time.Millisecond)
				p.mu.Lock()
				alive := p.conns[src]
				p.mu.Unlock()
				if !alive {
					return
				}
			}
			if down {
				total := p.down.Add(int64(n))
				if lim := p.cutDownAfter.Load(); lim > 0 && total > lim {
					p.cutDownAfter.Store(0)
					p.cut()
					return
				}
				if lim := p.dropUpAfter.Load(); lim > 0 && total > lim {
					p.dropUpAfter.Store(0)
					p.dropUp.Store(true)
				}
			} else if p.dropUp.Load() {
				continue
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	src.Close()
	dst.Close()
}

// cut drops every connection (the line broke).
func (p *flaky) cut() {
	p.mu.Lock()
	for c := range p.conns {
		c.Close()
	}
	p.conns = map[net.Conn]bool{}
	p.mu.Unlock()
}

type rig struct {
	t      *testing.T
	srv    *server.Server
	sdir   string
	ag     *agent.Agent
	spool  *winprint.Fake
	proxy  *flaky
	id     string
	port   int // local RAW port of the printer on the server
	listen int
	stop   context.CancelFunc
}

const rigPrinter = "Каса"

func startRigServer(t *testing.T, sdir string, listen int) (*server.Server, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := server.New(sdir, common.NewLogRing("", 300), noAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Run(ctx)
	waitFor(t, "сервер слухає", func() bool {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(listen), 200*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	return srv, cancel
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, sdir: t.TempDir(), listen: freePort(t)}
	common.SaveJSON(r.sdir+"/server.json", map[string]any{"listen": "127.0.0.1:" + strconv.Itoa(r.listen),
		"web": "127.0.0.1:" + strconv.Itoa(freePort(t)), "first_port": freePort(t), "wg_mode": "off"})
	r.srv, r.stop = startRigServer(t, r.sdir, r.listen)
	t.Cleanup(func() { r.stop() })
	r.proxy = newFlaky(t, "127.0.0.1:"+strconv.Itoa(r.listen))
	if err := r.srv.SetPublicAddr(r.proxy.addr()); err != nil {
		t.Fatal(err)
	}
	id, code, err := r.srv.AddAgent("shop")
	if err != nil {
		t.Fatal(err)
	}
	r.id = id
	r.spool = &winprint.Fake{Printers: []winprint.PrinterInfo{{Name: rigPrinter}}}
	adir := t.TempDir()
	common.SaveJSON(adir+"/agent.json", map[string]any{"web": "127.0.0.1:" + strconv.Itoa(freePort(t))})
	r.ag, err = agent.New(adir, common.NewLogRing("", 300), r.spool)
	if err != nil {
		t.Fatal(err)
	}
	r.ag.SetShared([]proto.Printer{{Name: rigPrinter, Alias: "kasa"}})
	r.ag.SetCode(code)
	actx, acancel := context.WithCancel(context.Background())
	t.Cleanup(acancel)
	go r.ag.Run(actx)
	r.waitOnline()
	waitFor(t, "порт принтера на сервері", func() bool {
		for _, p := range r.view().Printers {
			if p.Alias == "kasa" {
				r.port = p.Port
			}
		}
		return r.port != 0
	})
	return r
}

func (r *rig) view() server.AgentView {
	for _, a := range r.srv.State().Agents {
		if a.ID == r.id {
			return a
		}
	}
	return server.AgentView{}
}

func (r *rig) waitOnline() {
	r.t.Helper()
	// OwnKey: after its first connection the point reconnects once with its
	// own key; the test goes on with that session.
	waitLong(r.t, "точка на зв'язку", func() bool { v := r.view(); return v.Online && v.OwnKey && r.ag.Status().Connected })
}

func (r *rig) printed() [][]byte { return r.spool.Jobs(rigPrinter) }

func waitLong(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("не дочекався: %s", what)
}

// settled: the printed list has stopped growing.
func (r *rig) settled(want int) {
	r.t.Helper()
	waitLong(r.t, fmt.Sprintf("надруковано %d", want), func() bool { return len(r.printed()) >= want })
	waitLong(r.t, "черга сервера порожня", func() bool { return len(r.view().Jobs) == 0 })
	time.Sleep(500 * time.Millisecond)
	if n := len(r.printed()); n != want {
		r.t.Fatalf("надруковано %d завдань, мало бути %d", n, want)
	}
}

func receipt(i int) []byte { return []byte(fmt.Sprintf("\x1b@RECEIPT %04d\n\x1dV\x00", i)) }

// The line breaks in the middle of a large job: nothing half-printed, the
// job stays queued and is printed once, intact, after the point is back.
func TestResilienceCutMidJob(t *testing.T) {
	r := newRig(t)
	big := make([]byte, 2<<20)
	rand.Read(big)
	r.proxy.down.Store(0)
	r.proxy.cutDownAfter.Store(600 << 10)
	if err := sendRaw(r.port, big); err != nil {
		t.Fatal(err)
	}
	waitLong(t, "обрив посеред завдання", func() bool { return r.proxy.cutDownAfter.Load() == 0 })
	r.settled(1)
	if !bytes.Equal(r.printed()[0], big) {
		t.Fatal("завдання після обриву дійшло зі змінами")
	}
}

// The point prints, but its confirmation never reaches the server: the
// server sends the job again after the reconnect and the point does not
// print it a second time.
func TestResilienceLostConfirmation(t *testing.T) {
	r := newRig(t)
	// The server checks the line before a job, so the line must die after
	// the job has started to flow: swallow the point's side from then on.
	job := make([]byte, 60<<10)
	rand.Read(job)
	r.proxy.down.Store(0)
	r.proxy.dropUpAfter.Store(20 << 10)
	if err := sendRaw(r.port, job); err != nil {
		t.Fatal(err)
	}
	waitLong(t, "точка надрукувала", func() bool { return len(r.printed()) == 1 })
	if !r.proxy.dropUp.Load() {
		t.Fatal("підтвердження не було заглушено — тест нічого не перевірив")
	}
	if n := len(r.view().Jobs); n != 1 {
		t.Fatalf("без підтвердження завдання мало лишитися в черзі сервера, там %d", n)
	}
	r.proxy.dropUp.Store(false)
	r.proxy.cut()
	r.settled(1)
	if !bytes.Equal(r.printed()[0], job) {
		t.Fatal("завдання дійшло зі змінами")
	}
}

// The server restarts with jobs in its queue (the point is away): after the
// restart they all arrive, in the order they were sent.
func TestResilienceServerRestartKeepsQueue(t *testing.T) {
	r := newRig(t)
	r.proxy.refuse.Store(true)
	r.proxy.cut()
	waitLong(t, "точка не на зв'язку", func() bool { return !r.view().Online })
	const n = 5
	for i := 1; i <= n; i++ {
		if err := sendRaw(r.port, receipt(i)); err != nil {
			t.Fatal(err)
		}
		i := i
		waitLong(t, "завдання в черзі", func() bool { return len(r.view().Jobs) == i })
	}
	r.stop()
	waitLong(t, "сервер зупинився", func() bool {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(r.listen), 200*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err != nil
	})
	r.srv, r.stop = startRigServer(t, r.sdir, r.listen)
	if got := len(r.view().Jobs); got != n {
		t.Fatalf("після перезапуску в черзі %d завдань, мало бути %d", got, n)
	}
	r.proxy.refuse.Store(false)
	r.settled(n)
	for i, got := range r.printed() {
		if !bytes.Equal(got, receipt(i+1)) {
			t.Fatalf("порядок порушено: на місці %d — %q", i+1, got)
		}
	}
}

// The point hangs: the connection is there but nothing answers. The server
// notices on the next job, keeps the job, and delivers it once the point is
// really back.
func TestResilienceHungPoint(t *testing.T) {
	r := newRig(t)
	r.proxy.frozen.Store(true)
	if err := sendRaw(r.port, receipt(1)); err != nil {
		t.Fatal(err)
	}
	waitLong(t, "сервер помітив, що точка не відповідає", func() bool { return !r.view().Online })
	if len(r.printed()) != 0 || len(r.view().Jobs) != 1 {
		t.Fatalf("завдання мало чекати: надруковано %d, у черзі %d", len(r.printed()), len(r.view().Jobs))
	}
	r.proxy.frozen.Store(false)
	r.proxy.cut()
	r.settled(1)
}

// The line flaps while receipts keep coming: every receipt is printed
// exactly once and receipts of one printer keep their order.
func TestResilienceReconnectStorm(t *testing.T) {
	r := newRig(t)
	const n = 40
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 12; i++ {
			time.Sleep(250 * time.Millisecond)
			r.proxy.cut()
		}
	}()
	for i := 1; i <= n; i++ {
		if err := sendRaw(r.port, receipt(i)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(80 * time.Millisecond)
	}
	<-done
	r.settled(n)
	r.checkOrder(n)
}

// checkOrder: receipts 1..n were printed exactly once, in order. On failure
// it says what exactly went wrong: a repeat, a loss or a swap.
func (r *rig) checkOrder(n int) {
	r.t.Helper()
	var order []int
	seen := map[int]int{}
	for _, got := range r.printed() {
		k := 0
		for i := 1; i <= n; i++ {
			if bytes.Equal(got, receipt(i)) {
				k = i
			}
		}
		order = append(order, k)
		seen[k]++
	}
	bad := false
	for i, k := range order {
		if k != i+1 {
			bad = true
		}
	}
	if !bad {
		return
	}
	var twice, lost []int
	for i := 1; i <= n; i++ {
		if seen[i] > 1 {
			twice = append(twice, i)
		}
		if seen[i] == 0 {
			lost = append(lost, i)
		}
	}
	for _, l := range r.srv.State().Log {
		r.t.Log("SERVER ", l)
	}
	for _, l := range r.ag.Log.Lines() {
		r.t.Log("POINT  ", l)
	}
	r.t.Fatalf("порядок друку: %v\nдвічі: %v, загублено: %v", order, twice, lost)
}

// A job that is still arriving from Windows is not overtaken by a later job
// for the same printer (measured before the fix: the later one printed first).
func TestResilienceOrderWhileArriving(t *testing.T) {
	r := newRig(t)
	a, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(r.port))
	if err != nil {
		t.Fatal(err)
	}
	first := receipt(1)
	a.Write(first[:10])
	time.Sleep(300 * time.Millisecond)
	if err := sendRaw(r.port, receipt(2)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if n := len(r.printed()); n != 0 {
		t.Fatalf("пізніший чек надруковано, поки раніший ще надходив (%d)", n)
	}
	a.Write(first[10:])
	a.(*net.TCPConn).CloseWrite()
	a.Close()
	r.settled(2)
	r.checkOrder(2)
}

// The earlier job breaks off (Windows will resend it): the later one is not
// stuck behind it.
func TestResilienceBrokenArrivalDoesNotBlock(t *testing.T) {
	r := newRig(t)
	a, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(r.port))
	if err != nil {
		t.Fatal(err)
	}
	a.Write([]byte("half a receipt"))
	time.Sleep(200 * time.Millisecond)
	if err := sendRaw(r.port, receipt(2)); err != nil {
		t.Fatal(err)
	}
	a.(*net.TCPConn).SetLinger(0) // reset: the job never completes
	a.Close()
	waitLong(t, "пізніший чек надруковано", func() bool { return len(r.printed()) >= 1 })
	if !bytes.Equal(r.printed()[0], receipt(2)) {
		t.Fatalf("надруковано не той чек: %q", r.printed()[0])
	}
}

// The earlier job hangs without sending anything: after ArrivingQuiet the
// later job goes, so one stuck connection does not stop the till.
func TestResilienceStuckArrivalReleases(t *testing.T) {
	old := server.ArrivingQuiet
	server.ArrivingQuiet = 2 * time.Second
	defer func() { server.ArrivingQuiet = old }()
	r := newRig(t)
	a, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(r.port))
	if err != nil {
		t.Fatal(err)
	}
	// The stuck connection is reset, not closed: a plain close would turn
	// "stuck" into a finished job, and the server would be writing it into the
	// spool while the test folder is being removed. The test waits until the
	// server has dropped the job's files.
	defer func() {
		a.(*net.TCPConn).SetLinger(0)
		a.Close()
		waitFor(t, "черга сервера порожня після скидання завислого з'єднання", func() bool {
			left, err := os.ReadDir(r.sdir + "/spool")
			return err != nil || len(left) == 0
		})
	}()
	a.Write([]byte("stuck"))
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := sendRaw(r.port, receipt(2)); err != nil {
		t.Fatal(err)
	}
	waitLong(t, "пізніший чек надруковано попри завислий", func() bool { return len(r.printed()) == 1 })
	if d := time.Since(start); d < 1500*time.Millisecond {
		t.Fatalf("пізніший чек пішов за %v — раніше, ніж минула пауза очікування", d)
	}
}
