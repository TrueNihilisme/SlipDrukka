package wgtun

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func freeUDP(t *testing.T) int {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func TestKeys(t *testing.T) {
	a, b := PointPublic("secret-1"), PointPublic("secret-1")
	if a != b || a.IsZero() {
		t.Fatal("ключ точки має бути однаковим з того самого коду")
	}
	if PointPublic("secret-2") == a {
		t.Fatal("різні коди дали однаковий ключ")
	}
	k, err := ParseKey(a.String())
	if err != nil || k != a {
		t.Fatal("ключ не читається назад")
	}
	if PointAddr(a) == ServerAddr || !prefix.Contains(PointAddr(a)) {
		t.Fatalf("адреса точки %v", PointAddr(a))
	}
}

// A point reaches the server through the tunnel; an unknown point does not.
func TestTunnel(t *testing.T) {
	spriv, _ := NewPrivate()
	port := freeUDP(t)
	srv, err := Listen(spriv, port)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ppriv := PointPrivate("point-secret")
	if err := srv.SetPeers([]Key{Public(ppriv)}); err != nil {
		t.Fatal(err)
	}
	ln, err := srv.ListenTCP(port)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type seen struct {
		pub  Key
		ip   string
		line string
	}
	got := make(chan seen, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		pub, ep, _ := srv.Peer(c.RemoteAddr())
		line, _ := bufio.NewReader(c).ReadString('\n')
		c.Write([]byte("pong\n"))
		got <- seen{pub, ep.Addr().String(), line}
		c.Close()
	}()

	addr := "127.0.0.1:" + strconv.Itoa(port)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, ppriv, Public(spriv), addr)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("ping\n"))
	reply, _ := bufio.NewReader(c).ReadString('\n')
	c.Close()
	if reply != "pong\n" {
		t.Fatalf("відповідь %q", reply)
	}
	s := <-got
	if s.pub != Public(ppriv) || s.ip != "127.0.0.1" || s.line != "ping\n" {
		t.Fatalf("сервер побачив %+v", s)
	}

	// a point the server does not know gets no answer at all
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if c, err := Dial(ctx2, PointPrivate("stranger"), Public(spriv), addr); err == nil {
		c.Close()
		t.Fatal("невідомий ключ пройшов")
	}
}
