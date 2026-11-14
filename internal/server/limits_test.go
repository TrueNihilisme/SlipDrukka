package server

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
)

func freeLocalPort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// What a point reports is not trusted: it cannot make the server open ports
// without end, and running out of ports does not hang the server.
func TestPrinterLimitPerPoint(t *testing.T) {
	old := MaxPrintersPerPoint
	MaxPrintersPerPoint = 3
	defer func() { MaxPrintersPerPoint = old }()

	var many []proto.Printer
	for i := 0; i < 50; i++ {
		many = append(many, proto.Printer{Name: "P" + strconv.Itoa(i), Alias: "p" + strconv.Itoa(i)})
	}
	if n := len(cleanPrinters(many)); n != 3 {
		t.Fatalf("прийнято %d принтерів, межа 3", n)
	}

	s, err := New(t.TempDir(), common.NewLogRing("", 50), nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.FirstPort = freeLocalPort(t)
	for i := 0; i < 3; i++ {
		if s.ensurePortLocked("a1", "p"+strconv.Itoa(i)) == 0 {
			t.Fatalf("принтер %d не отримав порт", i)
		}
	}
	// New names keep arriving (a point that renames its printers on every
	// report): no more ports for this point, others are not affected.
	for i := 3; i < 20; i++ {
		if p := s.ensurePortLocked("a1", "p"+strconv.Itoa(i)); p != 0 {
			t.Fatalf("понад межу видано порт %d", p)
		}
	}
	if len(s.cfg.Ports) != 3 {
		t.Fatalf("закріплено %d портів, мало бути 3", len(s.cfg.Ports))
	}
	if s.ensurePortLocked("a2", "p0") == 0 {
		t.Fatal("інша точка лишилась без порту")
	}
	if !strings.Contains(strings.Join(s.Log.Lines(), "\n"), "не отримав порт") {
		t.Fatal("у журналі немає запису про відмову")
	}

	// No free port left: an answer, not an endless loop.
	s.cfg.FirstPort = 65535
	s.cfg.Ports["a9/x"] = 65535
	done := make(chan int, 1)
	go func() { done <- s.ensurePortLocked("a3", "p0") }()
	select {
	case p := <-done:
		if p != 0 {
			t.Fatalf("видано порт %d за межею", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("сервер завис у пошуку вільного порту")
	}
}

// All queued jobs together have a size limit, so a local user cannot fill
// the disk through a printer port of a shop that is offline.
func TestSpoolLimit(t *testing.T) {
	old := MaxSpoolBytes
	MaxSpoolBytes = 1000
	defer func() { MaxSpoolBytes = old }()

	s, err := New(t.TempDir(), common.NewLogRing("", 50), nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	port := freeLocalPort(t)
	s.mu.Lock()
	s.cfg.Ports["a1/p"] = port
	s.startPortLocked("a1/p", port)
	s.mu.Unlock()

	send := func(n int) {
		c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			t.Fatal(err)
		}
		c.Write(make([]byte, n))
		c.(*net.TCPConn).CloseWrite()
		buf := make([]byte, 1)
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		c.Read(buf) // returns when the server closes or resets the connection
		c.Close()
	}
	queued := func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.jobs)
	}
	send(600)
	deadline := time.Now().Add(5 * time.Second)
	for queued() != 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if queued() != 1 {
		t.Fatal("перше завдання не стало в чергу")
	}
	send(600) // 600 queued + 600 > 1000
	deadline = time.Now().Add(5 * time.Second)
	for !strings.Contains(strings.Join(s.Log.Lines(), "\n"), "черга сервера заповнена") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if queued() != 1 {
		t.Fatalf("у черзі %d завдань, друге мало бути відхилене", queued())
	}
	if !strings.Contains(strings.Join(s.Log.Lines(), "\n"), "черга сервера заповнена") {
		t.Fatal("у журналі немає запису про заповнену чергу")
	}
	if n := s.incoming.Load(); n != 0 {
		t.Fatalf("лічильник завдань, що приймаються: %d, мало бути 0", n)
	}
	// A small job still fits.
	send(300)
	deadline = time.Now().Add(5 * time.Second)
	for queued() != 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if queued() != 2 {
		t.Fatal("мале завдання не прийнято, хоча місце є")
	}
}

// Only the print spooler may feed a local printer port: a connection the
// check refuses does not become a job.
func TestLocalClientCheck(t *testing.T) {
	s, err := New(t.TempDir(), common.NewLogRing("", 50), nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	var allow atomic.Bool
	s.LocalClient = func(net.Conn) error {
		if allow.Load() {
			return nil
		}
		return errors.New("програма в сеансі користувача №3 (процес 4242)")
	}
	port := freeLocalPort(t)
	s.mu.Lock()
	s.cfg.Ports["a1/p"] = port
	s.startPortLocked("a1/p", port)
	s.mu.Unlock()
	send := func() {
		c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			t.Fatal(err)
		}
		c.Write([]byte("receipt"))
		c.(*net.TCPConn).CloseWrite()
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		c.Read(make([]byte, 1))
		c.Close()
	}
	queued := func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.jobs)
	}
	send()
	send()
	time.Sleep(200 * time.Millisecond)
	if queued() != 0 {
		t.Fatalf("відхилене з'єднання стало завданням (%d у черзі)", queued())
	}
	log := strings.Join(s.Log.Lines(), "\n")
	if strings.Count(log, "відхилено з'єднання з локальним портом") != 1 {
		t.Fatalf("у журналі має бути один запис про відмову:\n%s", log)
	}
	allow.Store(true)
	send()
	deadline := time.Now().Add(5 * time.Second)
	for queued() != 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if queued() != 1 {
		t.Fatal("завдання від спулера не прийнято")
	}
}
