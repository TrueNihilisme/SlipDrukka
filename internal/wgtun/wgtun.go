// Package wgtun is the optional WireGuard channel between a point and the
// server. It runs entirely inside the program: wireguard-go with a userspace
// network stack (gVisor netstack), so no network adapter, driver or route is
// created in Windows and other VPNs or WireGuard tunnels on the machine are
// not touched. The only thing visible from outside is one UDP port on the
// server. Inside the tunnel the usual TLS connection with the point's code
// runs unchanged, so WireGuard is an extra layer, not a replacement.
//
// Keys: a point's WireGuard key is derived from the secret in its connection
// code, so the server can compute the point's public key whenever it sees
// that secret (when issuing the code or on a TLS connection) and nothing new
// has to be handed out. Tunnel addresses are derived from public keys.
package wgtun

import (
	"context"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// Key is a Curve25519 key.
type Key [32]byte

// String is the base64 form used in config files and messages.
func (k Key) String() string { return base64.StdEncoding.EncodeToString(k[:]) }

func (k Key) hex() string { return hex.EncodeToString(k[:]) }

// IsZero reports an unset key.
func (k Key) IsZero() bool { return k == Key{} }

// ParseKey reads the base64 form.
func ParseKey(s string) (Key, error) {
	var k Key
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != len(k) {
		return k, errors.New("некоректний ключ WireGuard")
	}
	copy(k[:], b)
	return k, nil
}

func clamp(k *Key) {
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
}

// NewPrivate makes a random private key (the server's).
func NewPrivate() (Key, error) {
	var k Key
	if _, err := rand.Read(k[:]); err != nil {
		return k, err
	}
	clamp(&k)
	return k, nil
}

// Public returns the public key of a private one.
func Public(priv Key) Key {
	var pub Key
	out, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err == nil {
		copy(pub[:], out)
	}
	return pub
}

// PointPrivate derives a point's private key from the secret in its
// connection code.
func PointPrivate(codeSecret string) Key {
	var k Key
	b, err := hkdf.Key(sha256.New, []byte(codeSecret), nil, "slipdrukka wireguard point key v1", 32)
	if err == nil {
		copy(k[:], b)
	}
	clamp(&k)
	return k
}

// PointPublic is the public key of PointPrivate(codeSecret).
func PointPublic(codeSecret string) Key { return Public(PointPrivate(codeSecret)) }

// Tunnel addresses live in a private IPv6 range that exists only inside the
// program's own network stack.
var (
	prefix     = netip.MustParsePrefix("fd70:7072:7279::/64")
	ServerAddr = netip.MustParseAddr("fd70:7072:7279::1")
)

// PointAddr is a point's tunnel address, derived from its public key.
func PointAddr(pub Key) netip.Addr {
	sum := sha256.Sum256(pub[:])
	a := prefix.Addr().As16()
	copy(a[8:], sum[:8])
	if a[15] <= 1 && a[8]|a[9]|a[10]|a[11]|a[12]|a[13]|a[14] == 0 {
		a[15] = 2 // never the server's ::1
	}
	return netip.AddrFrom16(a)
}

const mtu = 1380 // leaves room for PPPoE and other overhead on shop lines

// ---------------------------------------------------------------- server

// Server is the WireGuard side of the server: one UDP port, one peer per
// point.
type Server struct {
	dev  *device.Device
	tnet *netstack.Net
	port int

	mu    sync.Mutex
	peers map[netip.Addr]Key // tunnel address -> public key
}

// Listen starts WireGuard on UDP port with the server's private key.
func Listen(priv Key, port int) (*Server, error) {
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{ServerAddr}, nil, mtu)
	if err != nil {
		return nil, err
	}
	dev := device.NewDevice(tdev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	if err := dev.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=%d\n", priv.hex(), port)); err != nil {
		dev.Close()
		return nil, err
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, err
	}
	return &Server{dev: dev, tnet: tnet, port: port, peers: map[netip.Addr]Key{}}, nil
}

// SetPeers sets the list of points allowed in. Only the difference is
// applied: peers that stay are not touched, so they keep their handshake
// and their current address (a full replace would make the server forget
// where every point is until it sends something again).
func (s *Server) SetPeers(pubs []Key) error {
	want := map[netip.Addr]Key{}
	for _, p := range pubs {
		if !p.IsZero() {
			want[PointAddr(p)] = p
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for a, p := range s.peers {
		if want[a] != p {
			fmt.Fprintf(&b, "public_key=%s\nremove=true\n", p.hex())
		}
	}
	for a, p := range want {
		if s.peers[a] != p {
			fmt.Fprintf(&b, "public_key=%s\nreplace_allowed_ips=true\nallowed_ip=%s/128\n", p.hex(), a)
		}
	}
	if b.Len() > 0 {
		if err := s.dev.IpcSet(b.String()); err != nil {
			return err
		}
	}
	s.peers = want
	return nil
}

// ListenTCP opens a TCP listener inside the tunnel.
func (s *Server) ListenTCP(port int) (net.Listener, error) {
	return s.tnet.ListenTCPAddrPort(netip.AddrPortFrom(ServerAddr, uint16(port)))
}

// Peer tells which point a tunnel connection came from: its public key and
// its real address on the internet (the UDP endpoint).
func (s *Server) Peer(remote net.Addr) (Key, netip.AddrPort, bool) {
	ap, err := netip.ParseAddrPort(remote.String())
	if err != nil {
		return Key{}, netip.AddrPort{}, false
	}
	s.mu.Lock()
	pub, ok := s.peers[ap.Addr()]
	s.mu.Unlock()
	if !ok {
		return Key{}, netip.AddrPort{}, false
	}
	ep, _ := s.endpoint(pub)
	return pub, ep, true
}

func (s *Server) endpoint(pub Key) (netip.AddrPort, bool) {
	cfg, err := s.dev.IpcGet()
	if err != nil {
		return netip.AddrPort{}, false
	}
	want := "public_key=" + pub.hex()
	in := false
	for _, line := range strings.Split(cfg, "\n") {
		switch {
		case strings.HasPrefix(line, "public_key="):
			in = line == want
		case in && strings.HasPrefix(line, "endpoint="):
			ap, err := netip.ParseAddrPort(strings.TrimPrefix(line, "endpoint="))
			if err != nil {
				return netip.AddrPort{}, false
			}
			return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), true
		}
	}
	return netip.AddrPort{}, false
}

// Port is the UDP port WireGuard listens on.
func (s *Server) Port() int { return s.port }

// Close stops WireGuard and frees the UDP port.
func (s *Server) Close() { s.dev.Close() }

// ---------------------------------------------------------------- point

// Client is a point's tunnel to the server. It is kept between connections:
// a new tunnel with the same key next to an old one would make the server
// send replies to whichever of them spoke last.
type Client struct {
	dev  *device.Device
	tnet *netstack.Net
	port int
	id   string // what the tunnel was built for (key, server, address)
}

// NewClient builds the tunnel to server ("host:port" from the code;
// WireGuard uses the same port number over UDP). Nothing is sent until Dial.
func NewClient(ctx context.Context, priv, serverPub Key, server string) (*Client, error) {
	host, portText, err := net.SplitHostPort(server)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("адреса %s: %v", host, err)
	}
	ip := ips[0].Unmap()
	for _, x := range ips { // prefer IPv4: shop lines rarely have working IPv6
		if x.Unmap().Is4() {
			ip = x.Unmap()
			break
		}
	}
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{PointAddr(Public(priv))}, nil, mtu)
	if err != nil {
		return nil, err
	}
	dev := device.NewDevice(tdev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	endpoint := netip.AddrPortFrom(ip, uint16(port))
	cfg := fmt.Sprintf("private_key=%s\npublic_key=%s\nendpoint=%s\nallowed_ip=%s/128\npersistent_keepalive_interval=25\n",
		priv.hex(), serverPub.hex(), endpoint, ServerAddr)
	if err := dev.IpcSet(cfg); err != nil {
		dev.Close()
		return nil, err
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, err
	}
	return &Client{dev: dev, tnet: tnet, port: port, id: Public(priv).String() + "|" + serverPub.String() + "|" + endpoint.String()}, nil
}

// Same tells whether the tunnel was built for these settings (the server's
// address may resolve differently over time).
func (c *Client) Same(priv, serverPub Key, endpoint netip.AddrPort) bool {
	return c.id == Public(priv).String()+"|"+serverPub.String()+"|"+endpoint.String()
}

// Dial opens a TCP connection to the server through the tunnel.
func (c *Client) Dial(ctx context.Context) (net.Conn, error) {
	conn, err := c.tnet.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(ServerAddr, uint16(c.port)))
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("сервер не відповів (UDP міг бути заблокований)")
		}
		return nil, err
	}
	return conn, nil
}

// Close stops the tunnel.
func (c *Client) Close() { c.dev.Close() }

// Dial is a one-off tunnel connection (tests, checks): the tunnel closes
// shortly after the connection does.
func Dial(ctx context.Context, priv, serverPub Key, server string) (net.Conn, error) {
	cl, err := NewClient(ctx, priv, serverPub, server)
	if err != nil {
		return nil, err
	}
	c, err := cl.Dial(ctx)
	if err != nil {
		cl.Close()
		return nil, err
	}
	return &oneOff{Conn: c, cl: cl}, nil
}

type oneOff struct {
	net.Conn
	cl   *Client
	once sync.Once
}

func (c *oneOff) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		go func() { // let the last data and the TCP close reach the server first
			time.Sleep(3 * time.Second)
			c.cl.Close()
		}()
	})
	return err
}
