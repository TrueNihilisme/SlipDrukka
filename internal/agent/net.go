package agent

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"github.com/hashicorp/yamux"

	"slipdrukka/internal/nettools"
	"slipdrukka/internal/proto"
)

// Network view of the point (0.17.0): its own addresses and router, the
// server's address, the round trip and the route to the server.

// remoteHost: the IP the connection to the server goes to. Through the
// WireGuard tunnel the connection's own address is inside the tunnel, so
// the server's name from the code is looked up instead.
func remoteHost(c net.Conn, via, addr string) string {
	if ta, ok := c.RemoteAddr().(*net.TCPAddr); ok && via == proto.ViaTCP {
		return ta.IP.String()
	}
	host := serverHost(addr)
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil || len(ips) == 0 {
		return ""
	}
	return ips[0]
}

func serverHost(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// measureRTT pings the server over the session until it ends.
func (a *Agent) measureRTT(sess *yamux.Session) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		if d, err := sess.Ping(); err == nil {
			ms := float64(d.Microseconds()) / 1000
			a.setStatus(func(s *Status) { s.RTTms = ms })
		}
		select {
		case <-sess.CloseChan():
			return
		case <-t.C:
		}
	}
}

// netInfo is kept for a few minutes: the router is read through PowerShell.
func (a *Agent) netInfo() *proto.NetInfo {
	a.netMu.Lock()
	defer a.netMu.Unlock()
	if a.netCache != nil && time.Since(a.netAt) < 5*time.Minute {
		n := *a.netCache
		a.fillServer(&n)
		return &n
	}
	n := &proto.NetInfo{Gateways: nettools.Gateways()}
	for _, i := range nettools.LocalAddrs() {
		n.Ifaces = append(n.Ifaces, proto.NetIface{Name: i.Name, Addrs: i.Addrs})
	}
	a.netCache, a.netAt = n, time.Now()
	c := *n
	a.fillServer(&c)
	return &c
}

func (a *Agent) fillServer(n *proto.NetInfo) {
	st := a.Status()
	n.ServerAddr, n.ServerIP = st.Server, st.ServerIP
}

// traceToServer runs a traceroute to the server's address from the code.
func (a *Agent) traceToServer(ctx context.Context) (json.RawMessage, error) {
	st := a.Status()
	target := st.ServerIP
	if target == "" {
		target = serverHost(st.Server)
	}
	hops, err := nettools.Trace(ctx, target)
	if err != nil {
		return nil, err
	}
	a.Log.Printf("трасування до сервера (%s): %d переходів", target, len(hops))
	return json.Marshal(map[string]any{"target": target, "hops": hops})
}
