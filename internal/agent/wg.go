package agent

// Choosing the channel to the server. With a WireGuard key from the server
// the point first tries the WireGuard tunnel (built into the program, no
// adapter in Windows); if that does not answer within wgTimeout it connects
// over plain TLS as before and tries WireGuard again a minute later, then
// every wgRetry. While on plain TLS it checks WireGuard on the same schedule
// and moves over when idle.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/hashicorp/yamux"

	"slipdrukka/internal/proto"
	"slipdrukka/internal/tlsutil"
	"slipdrukka/internal/wgtun"
)

var (
	wgTimeout    = 10 * time.Second
	wgRetry      = 30 * time.Minute
	wgFirstRetry = time.Minute // after the first failure: a one-off hiccup should not cost 30 min
)

// wgDelayLocked: how long to stay on plain TLS after WireGuard failed. a.mu held.
func (a *Agent) wgDelayLocked() time.Duration {
	if a.wgFails <= 1 {
		return wgFirstRetry
	}
	return wgRetry
}

// dial opens the TLS connection to the server and says which channel it
// went over.
func (a *Agent) dial(ctx context.Context, code proto.Code) (net.Conn, string, error) {
	a.mu.Lock()
	serverWG := a.cfg.ServerWG
	tryWG := serverWG != "" && time.Since(a.wgFailedAt) > a.wgDelayLocked()
	a.mu.Unlock()
	if tryWG {
		c, err := a.dialWG(ctx, code, serverWG)
		if err == nil {
			a.mu.Lock()
			a.wgFails = 0
			a.mu.Unlock()
			return c, proto.ViaWireGuard, nil
		}
		a.Log.Printf("WireGuard: %v — підключаюсь звичайним TLS", err)
		a.mu.Lock()
		a.wgFailedAt = time.Now()
		a.wgFails++
		a.mu.Unlock()
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", code.Addr, tlsutil.PinnedClientConfig(code.Fingerprint))
	if err != nil {
		// The server may accept only WireGuard (its TCP port closed): try
		// WireGuard again on the next attempt instead of waiting wgRetry.
		a.mu.Lock()
		a.wgFailedAt = time.Time{}
		a.mu.Unlock()
		return nil, "", fmt.Errorf("не вдалося підключитися до %s: %w", code.Addr, err)
	}
	return conn, proto.ViaTCP, nil
}

func (a *Agent) dialWG(ctx context.Context, code proto.Code, serverWG string) (net.Conn, error) {
	spub, err := wgtun.ParseKey(serverWG)
	if err != nil {
		return nil, err
	}
	wctx, cancel := context.WithTimeout(ctx, wgTimeout)
	defer cancel()
	cl, err := a.wgClient(wctx, code, spub)
	if err != nil {
		return nil, err
	}
	raw, err := cl.Dial(wctx)
	if err != nil {
		return nil, err
	}
	conn := tls.Client(raw, tlsutil.PinnedClientConfig(code.Fingerprint))
	conn.SetDeadline(time.Now().Add(wgTimeout))
	if err := conn.HandshakeContext(wctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("TLS у тунелі: %w", err)
	}
	conn.SetDeadline(time.Time{})
	return conn, nil
}

// wgClient returns the point's tunnel, building it again only when the
// code, the server key or the server's address changed.
func (a *Agent) wgClient(ctx context.Context, code proto.Code, spub wgtun.Key) (*wgtun.Client, error) {
	priv := wgtun.PointPrivate(code.Key)
	a.wgMu.Lock()
	defer a.wgMu.Unlock()
	if a.wgc != nil {
		if ep, err := resolveEndpoint(ctx, code.Addr); err == nil && a.wgc.Same(priv, spub, ep) {
			return a.wgc, nil
		}
		a.wgc.Close()
		a.wgc = nil
	}
	cl, err := wgtun.NewClient(ctx, priv, spub, code.Addr)
	if err != nil {
		return nil, err
	}
	a.wgc = cl
	return cl, nil
}

// dropWG closes the tunnel (WireGuard switched off, code changed).
func (a *Agent) dropWG() {
	a.wgMu.Lock()
	if a.wgc != nil {
		a.wgc.Close()
		a.wgc = nil
	}
	a.wgMu.Unlock()
}

func resolveEndpoint(ctx context.Context, server string) (netip.AddrPort, error) {
	host, portText, err := net.SplitHostPort(server)
	if err != nil {
		return netip.AddrPort{}, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return netip.AddrPort{}, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return netip.AddrPort{}, fmt.Errorf("адреса %s: %v", host, err)
	}
	ip := ips[0].Unmap()
	for _, x := range ips {
		if x.Unmap().Is4() {
			ip = x.Unmap()
			break
		}
	}
	return netip.AddrPortFrom(ip, uint16(port)), nil
}

// watchWG runs while the point is on plain TLS: every wgRetry it checks
// whether WireGuard works now and, when nothing is printing, asks the
// session to reconnect through it.
func (a *Agent) watchWG(sess *yamux.Session, code proto.Code, upgrade chan<- struct{}) {
	for {
		a.mu.Lock()
		wait := a.wgDelayLocked()
		a.mu.Unlock()
		select {
		case <-sess.CloseChan():
			return
		case <-time.After(wait):
		}
		a.mu.Lock()
		serverWG := a.cfg.ServerWG
		a.mu.Unlock()
		if serverWG == "" {
			return
		}
		c, err := a.dialWG(context.Background(), code, serverWG)
		if err != nil {
			a.mu.Lock()
			a.wgFails++
			a.mu.Unlock()
			continue
		}
		c.Close()
		for a.busy.Load() > 0 { // never cut a job in the middle
			select {
			case <-sess.CloseChan():
				return
			case <-time.After(2 * time.Second):
			}
		}
		a.mu.Lock()
		a.wgFailedAt, a.wgFails = time.Time{}, 0
		a.mu.Unlock()
		select {
		case upgrade <- struct{}{}:
		default:
		}
		return
	}
}
