package server

import (
	"crypto/tls"
	"testing"
	"time"

	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
)

// One-time codes: the code opens the door once; the point gets its own key.
func TestOneTimeCode(t *testing.T) {
	s, _ := newGuardServer(t)
	id, code := addPoint(t, s, "Каса")
	if err := s.listenAgents("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer s.agentLn.Close()
	addr := s.agentLn.Addr().String()
	hello := func(k string, caps ...string) proto.Welcome {
		t.Helper()
		c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		if err := proto.WriteLine(c, proto.Hello{Proto: proto.Version, Key: k, Hostname: "t", Caps: caps}); err != nil {
			t.Fatal(err)
		}
		var w proto.Welcome
		if err := proto.ReadLine(c, &w); err != nil {
			t.Fatal(err)
		}
		return w
	}
	ownKey := func() bool {
		for _, a := range s.State().Agents {
			if a.ID == id {
				return a.OwnKey
			}
		}
		return false
	}

	// An old point (no capability) keeps using its code.
	if w := hello(code); !w.OK || w.NewKey != "" {
		t.Fatalf("old point: %+v", w)
	}
	// A new point gets its own key; until it uses it, the code still works
	// and gets the same key again (the point may have lost it).
	w1 := hello(code, proto.CapOwnKey)
	if !w1.OK || w1.NewKey == "" || w1.NewKey == code {
		t.Fatalf("first connection with the code: %+v", w1)
	}
	if w2 := hello(code, proto.CapOwnKey); !w2.OK || w2.NewKey != w1.NewKey {
		t.Fatalf("code again before the key was used: %+v", w2)
	}
	if ownKey() {
		t.Fatal("own key confirmed before the point used it")
	}
	// The first connection with the new key confirms it...
	if w := hello(w1.NewKey, proto.CapOwnKey); !w.OK || w.NewKey != "" {
		t.Fatalf("connection with the own key: %+v", w)
	}
	if !ownKey() {
		t.Fatal("own key not confirmed")
	}
	// ...and from then on the code is refused, the key works.
	if w := hello(code, proto.CapOwnKey); w.OK {
		t.Fatalf("code still works after the own key was confirmed: %+v", w)
	}
	if w := hello(w1.NewKey, proto.CapOwnKey); !w.OK {
		t.Fatalf("own key refused: %+v", w)
	}
	// It survives a restart of the server: the config on disk says the same.
	// The file is read directly — a second Server in the folder of a running
	// one would write the same files at the same time.
	var disk Config
	if err := common.LoadJSON(s.cfgPath(), &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Agents) == 0 {
		t.Fatal("no points in the config on disk")
	}
	for _, a := range disk.Agents {
		if a.ID == id && (!a.OwnKey || a.PendingKey != "") {
			t.Fatalf("after restart: own=%v pending=%q", a.OwnKey, a.PendingKey)
		}
	}
	// A new code from the panel works again, once.
	newCode, err := s.Rekey(id)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := proto.ParseCode(newCode)
	if w := hello(c.Key, proto.CapOwnKey); !w.OK || w.NewKey == "" {
		t.Fatalf("re-issued code: %+v", w)
	}
	if w := hello(w1.NewKey, proto.CapOwnKey); w.OK {
		t.Fatalf("old own key works after a new code: %+v", w)
	}
}
