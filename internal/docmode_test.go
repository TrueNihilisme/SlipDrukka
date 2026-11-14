package internal_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"strconv"
	"testing"

	"slipdrukka/internal/agent"
	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/server"
	"slipdrukka/internal/winprint"
)

// spyFake tells raw jobs and document jobs apart.
type spyFake struct {
	*winprint.Fake
	docs int
}

func (s *spyFake) PrintXPS(printer, doc, path string) error {
	s.docs++
	return s.Fake.PrintXPS(printer, doc, path)
}

func TestDocumentMode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sdir := t.TempDir()
	agentPort := freePort(t)
	common.SaveJSON(sdir+"/server.json", map[string]any{"listen": "127.0.0.1:" + strconv.Itoa(agentPort),
		"web": "127.0.0.1:" + strconv.Itoa(freePort(t)), "first_port": freePort(t), "wg_mode": testWGMode})
	srv, _ := server.New(sdir, common.NewLogRing("", 50), noAdmin{})
	srv.SetPublicAddr("127.0.0.1:" + strconv.Itoa(agentPort))
	go srv.Run(ctx)
	_, code, _ := srv.AddAgent("office")

	spool := &spyFake{Fake: &winprint.Fake{Printers: []winprint.PrinterInfo{{Name: "HP LaserJet 1018"}}}}
	adir := t.TempDir()
	common.SaveJSON(adir+"/agent.json", map[string]any{"web": "127.0.0.1:" + strconv.Itoa(freePort(t))})
	ag, _ := agent.New(adir, common.NewLogRing("", 50), spool)
	if err := ag.SetShared([]proto.Printer{{Name: "HP LaserJet 1018", Alias: "hp1018", Mode: "bogus"}}); err == nil {
		t.Fatal("невідомий режим прийнято")
	}
	ag.SetShared([]proto.Printer{{Name: "HP LaserJet 1018", Alias: "hp1018", Mode: proto.ModeXPS}})
	ag.SetCode(code)
	go ag.Run(ctx)
	var port int
	waitFor(t, "принтер у режимі документ на сервері", func() bool {
		for _, a := range srv.State().Agents {
			for _, p := range a.Printers {
				if p.Alias == "hp1018" && p.Offered && p.Mode == proto.ModeXPS {
					port = p.Port
					return true
				}
			}
		}
		return false
	})
	doc := make([]byte, 200000)
	rand.Read(doc)
	copy(doc, "PK\x03\x04") // looks like the zip an XPS document is
	if err := sendRaw(port, doc); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "документ надруковано", func() bool { return len(spool.Jobs("HP LaserJet 1018")) == 1 })
	if spool.docs != 1 {
		t.Fatalf("документ пішов не тим шляхом (друк документа викликано %d разів)", spool.docs)
	}
	if !bytes.Equal(spool.Jobs("HP LaserJet 1018")[0], doc) {
		t.Fatal("документ дійшов зі змінами")
	}
}
