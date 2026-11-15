package server

// Optional WireGuard channel for points (package wgtun): runs inside the
// program on one UDP port with the same number as the TCP port for points.
// Modes: on (default; points use WireGuard when it works and plain TLS over
// TCP otherwise), off, only (the TCP port is closed; a point must have
// connected once before, or have a code issued by this version, so that the
// server knows its WireGuard key).

import (
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/wgtun"
)

const (
	WGOn   = ""
	WGOff  = "off"
	WGOnly = "only"

	fwAgentsWG = "SlipDrukka Server WireGuard"

	wgGrace = 3 * time.Second
)

// loadWGKey reads the server's WireGuard private key, creating it once.
func loadWGKey(dir string) (wgtun.Key, error) {
	path := common.Join(dir, "wg.key")
	if b, err := os.ReadFile(path); err == nil {
		return wgtun.ParseKey(string(b))
	}
	k, err := wgtun.NewPrivate()
	if err != nil {
		return k, err
	}
	return k, os.WriteFile(path, []byte(k.String()+"\n"), 0o600)
}

func (s *Server) wgPublic() string { return wgtun.Public(s.wgKey).String() }

func (s *Server) wgModeLocked() string { return s.cfg.WGMode }

// startWG (re)starts WireGuard on the points port, or stops it in "off"
// mode. s.mu must not be held.
func (s *Server) startWG() error {
	p := s.ports()
	s.mu.Lock()
	mode := s.cfg.WGMode
	old, oldLn := s.wg, s.wgLn
	s.wg, s.wgLn = nil, nil
	s.mu.Unlock()
	if oldLn != nil {
		oldLn.Close()
	}
	if old != nil {
		old.Close()
	}
	if mode == WGOff {
		return nil
	}
	wg, err := wgtun.Listen(s.wgKey, p.AgentPort)
	if err != nil {
		return err
	}
	ln, err := wg.ListenTCP(p.AgentPort)
	if err != nil {
		wg.Close()
		return err
	}
	s.mu.Lock()
	s.wg, s.wgLn = wg, ln
	err = s.updatePeersLocked()
	s.mu.Unlock()
	go s.acceptWG(wg, ln)
	return err
}

// updatePeersLocked tells WireGuard which points may come in.
func (s *Server) updatePeersLocked() error {
	if s.wg == nil {
		return nil
	}
	var keys []wgtun.Key
	for _, a := range s.cfg.Agents {
		if k, err := wgtun.ParseKey(a.WGPub); err == nil {
			keys = append(keys, k)
		}
		if k, err := wgtun.ParseKey(pendingWGPub(a)); err == nil {
			keys = append(keys, k) // one-time codes: the key it is about to use
		}
	}
	now := time.Now()
	for pub, until := range s.wgGrace {
		if now.After(until) {
			delete(s.wgGrace, pub)
		} else if k, err := wgtun.ParseKey(pub); err == nil {
			keys = append(keys, k)
		}
	}
	return s.wg.SetPeers(keys)
}

func (s *Server) acceptWG(wg *wgtun.Server, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		pub, ep, ok := wg.Peer(c.RemoteAddr())
		if !ok {
			c.Close()
			continue
		}
		ip := ep.Addr()
		// WireGuard already proved the point's key; the allow-lists still
		// apply to its real address.
		if !s.sourceAllowed(ip) {
			s.noteRefused(ip, "адреса не в списку дозволених (WireGuard)")
			c.Close()
			continue
		}
		release, ok := s.enterHandshakeWG(ip)
		if !ok {
			c.Close()
			continue
		}
		tc := tls.Server(&viaConn{Conn: c, remote: ep}, s.tlsConfig())
		go s.handleAgent(tc, ip, false, true, release, &pub)
	}
}

// viaConn shows the point's real address instead of its tunnel address.
type viaConn struct {
	net.Conn
	remote netip.AddrPort
}

func (c *viaConn) RemoteAddr() net.Addr { return net.TCPAddrFromAddrPort(c.remote) }

// SetWGMode switches the WireGuard channel and, in "only" mode, closes the
// TCP port for points.
func (s *Server) SetWGMode(mode string) error {
	if mode != WGOn && mode != WGOff && mode != WGOnly {
		return errors.New("невідомий режим WireGuard")
	}
	if mode == WGOnly {
		if missing := s.pointsWithoutWG(); len(missing) > 0 {
			return errors.New("ще не можна вимкнути звичайне підключення: сервер не знає ключа WireGuard точок " +
				strings.Join(missing, ", ") + ". Їм треба один раз підключитися в режимі «увімкнено» або отримати новий код")
		}
	}
	s.mu.Lock()
	old := s.cfg.WGMode
	s.cfg.WGMode = mode
	err := s.save()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if (old == WGOff) != (mode == WGOff) {
		if err := s.startWG(); err != nil {
			s.Log.Printf("ПОМИЛКА WireGuard: %v", err)
			return err
		}
	}
	if err := s.applyTCP(); err != nil {
		return err
	}
	s.applyFirewall()
	s.Log.Printf("WireGuard: %s", wgModeText(mode))
	return nil
}

func wgModeText(m string) string {
	switch m {
	case WGOff:
		return "вимкнено, точки підключаються лише звичайним TLS"
	case WGOnly:
		return "лише WireGuard, порт TCP для точок закрито"
	}
	return "увімкнено, точки, яким UDP недоступний, підключаються звичайним TLS"
}

// applyTCP opens or closes the TCP port for points according to the mode.
func (s *Server) applyTCP() error {
	s.mu.Lock()
	only := s.cfg.WGMode == WGOnly
	ln := s.agentLn
	addr := s.cfg.Listen
	s.mu.Unlock()
	if only && ln != nil {
		s.mu.Lock()
		s.agentLn = nil
		s.mu.Unlock()
		ln.Close()
		return nil
	}
	if !only && ln == nil {
		return s.listenAgents(addr)
	}
	return nil
}

func (s *Server) pointsWithoutWG() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, a := range s.cfg.Agents {
		if a.WGPub == "" {
			out = append(out, a.Name)
		}
	}
	return out
}

// retireWGKeyLocked removes a point's old WireGuard key a moment later:
// after a code is re-issued or a point deleted, its session is closed first,
// and the close must still reach the point through the tunnel with that key.
func (s *Server) retireWGKeyLocked(pub string) {
	if pub == "" {
		return
	}
	if s.wgGrace == nil {
		s.wgGrace = map[string]time.Time{}
	}
	s.wgGrace[pub] = time.Now().Add(wgGrace)
	go func() {
		time.Sleep(wgGrace + 100*time.Millisecond)
		s.mu.Lock()
		err := s.updatePeersLocked()
		s.mu.Unlock()
		if err != nil {
			s.Log.Printf("ПОМИЛКА WireGuard: %v", err)
		}
	}()
}

// learnWGKeyLocked records the point's WireGuard key, derived from the code
// secret it has just shown.
func (s *Server) learnWGKeyLocked(a *AgentConfig, codeSecret string) bool {
	k := wgtun.PointPublic(codeSecret).String()
	if a.WGPub == k {
		return false
	}
	s.retireWGKeyLocked(a.WGPub)
	a.WGPub = k
	if err := s.updatePeersLocked(); err != nil {
		s.Log.Printf("ПОМИЛКА WireGuard: %v", err)
	}
	return true
}

// welcomeWGLocked is what the point is told about WireGuard.
func (s *Server) welcomeWGLocked() string {
	if s.cfg.WGMode == WGOff {
		return ""
	}
	return s.wgPublic()
}

func viaName(peer *wgtun.Key) string {
	if peer != nil {
		return proto.ViaWireGuard
	}
	return proto.ViaTCP
}
