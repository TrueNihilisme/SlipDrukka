package common

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// WebServer is an admin page that can move to another address while
// running. The new address is bound first; only when that works is the old
// one closed, so a busy port never leaves the page unreachable.
type WebServer struct {
	mu      sync.Mutex
	handler http.Handler
	srv     *http.Server
	addr    string
}

func NewWebServer(h http.Handler) *WebServer {
	return &WebServer{handler: SecurityHeaders(WithLang(h))}
}

// Start binds addr and serves on it. If the server already runs elsewhere,
// the old address keeps working for a moment so the reply that triggered the
// move still reaches the browser.
func (w *WebServer) Start(addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: w.handler, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(l)
	w.mu.Lock()
	old := w.srv
	w.srv, w.addr = srv, addr
	w.mu.Unlock()
	if old != nil {
		go func() { time.Sleep(1500 * time.Millisecond); old.Close() }()
	}
	return nil
}

func (w *WebServer) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.srv != nil {
		w.srv.Close()
		w.srv = nil
	}
}

func (w *WebServer) Addr() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.addr
}

// PanelAddr builds the bind address of an admin page.
func PanelAddr(lan bool, port int) string {
	host := "127.0.0.1"
	if lan {
		host = "0.0.0.0"
	}
	return host + ":" + strconv.Itoa(port)
}

// ParsePanelAddr is the reverse of PanelAddr.
func ParsePanelAddr(addr string) (lan bool, port int) {
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		return false, 0
	}
	port, _ = strconv.Atoi(p)
	return host == "0.0.0.0" || host == "" || host == "::", port
}

// CheckPort validates a TCP port number typed into a form.
func CheckPort(p int, what string) error {
	if p < 1 || p > 65535 {
		return errors.New(what + ": порт має бути від 1 до 65535")
	}
	return nil
}
