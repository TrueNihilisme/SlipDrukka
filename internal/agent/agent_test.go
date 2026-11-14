package agent

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"testing"

	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/winprint"
	"slipdrukka/internal/winps"
)

// A job re-sent with the same ID (confirmation lost on the way back) must be
// confirmed again but printed only once.
func TestDuplicateJobPrintedOnce(t *testing.T) {
	spool := &winprint.Fake{Printers: []winprint.PrinterInfo{{Name: "P"}}}
	a, err := New(t.TempDir(), common.NewLogRing("", 10), spool)
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.Shared = []proto.Printer{{Name: "P", Alias: "p"}}
	for i := 0; i < 2; i++ {
		srv, cli := net.Pipe()
		go a.handleJob(cli)
		go func() {
			proto.WriteLine(srv, proto.JobHeader{ID: "same-id", Printer: "P"})
			proto.WriteFrame(srv, []byte("receipt"))
			proto.WriteFrame(srv, nil)
		}()
		var res proto.JobResult
		if err := proto.ReadLine(bufio.NewReader(srv), &res); err != nil || !res.OK {
			t.Fatalf("спроба %d: %v %+v", i+1, err, res)
		}
		srv.Close()
	}
	if n := len(spool.Jobs("P")); n != 1 {
		t.Fatalf("надруковано %d разів", n)
	}
	// The guard must survive a restart.
	b, _ := New(a.dir, common.NewLogRing("", 10), spool)
	if _, ok := b.done["same-id"]; !ok {
		t.Fatal("після перезапуску агент забув надруковані завдання")
	}
}

type fakeAdmin struct {
	calls []string
	spool *winprint.Fake
}

func (f *fakeAdmin) Drivers() ([]string, error) { return []string{"Generic / Text Only"}, nil }
func (f *fakeAdmin) AddPrinterOnPort(name, port, driver string) error {
	f.calls = append(f.calls, name+"|"+port+"|"+driver)
	f.spool.Printers = append(f.spool.Printers, winprint.PrinterInfo{Name: name, Driver: driver, Port: port})
	return nil
}
func (f *fakeAdmin) RenamePrinter(old, new string) error {
	for i := range f.spool.Printers {
		if f.spool.Printers[i].Name == old {
			f.spool.Printers[i].Name = new
		}
	}
	return nil
}
func (f *fakeAdmin) RemovePrinter(name string) error {
	var keep []winprint.PrinterInfo
	for _, p := range f.spool.Printers {
		if p.Name != name {
			keep = append(keep, p)
		}
	}
	f.spool.Printers = keep
	return nil
}
func (f *fakeAdmin) SetPrinterIP(name, host string, port int) error {
	for i := range f.spool.Printers {
		if f.spool.Printers[i].Name == name {
			f.spool.Printers[i].Port = "IP_" + host + "_" + strconv.Itoa(port)
		}
	}
	return nil
}
func (f *fakeAdmin) SetDriver(name, driver string) error {
	for i := range f.spool.Printers {
		if f.spool.Printers[i].Name == name {
			f.spool.Printers[i].Driver = driver
		}
	}
	return nil
}
func (f *fakeAdmin) Ports() ([]winps.PortDetail, []string, error) { return nil, nil, nil }
func (f *fakeAdmin) AddNetworkPrinter(name, host string, port int, driver string) error {
	f.calls = append(f.calls, name+"|"+host+"|"+strconv.Itoa(port)+"|"+driver)
	f.spool.Printers = append(f.spool.Printers, winprint.PrinterInfo{Name: name, Driver: driver, Port: "IP_" + host})
	return nil
}

func TestAddNetworkPrinter(t *testing.T) {
	spool := &winprint.Fake{}
	a, _ := New(t.TempDir(), common.NewLogRing("", 10), spool)
	fa := &fakeAdmin{spool: spool}
	a.Admin = fa
	for _, bad := range []NetPrinter{
		{Name: "a,b", Host: "192.168.0.5"},
		{Name: "ok", Host: "192.168.0.5; rm"},
		{Name: "ok", Host: "192.168.0.5", Port: 70000},
		{Name: "", Host: "192.168.0.5"},
	} {
		if err := a.AddNetworkPrinter(bad); err == nil {
			t.Fatalf("прийнято некоректне: %+v", bad)
		}
	}
	if len(fa.calls) != 0 {
		t.Fatal("некоректні дані дійшли до Windows")
	}
	if err := a.AddNetworkPrinter(NetPrinter{Name: "Етикетки XP-370B", Host: "192.168.5.35", Share: true}); err != nil {
		t.Fatal(err)
	}
	if len(fa.calls) != 1 || fa.calls[0] != "Етикетки XP-370B|192.168.5.35|9100|Generic / Text Only" {
		t.Fatalf("виклик Windows: %v", fa.calls)
	}
	if len(a.cfg.Shared) != 1 || a.cfg.Shared[0].Name != "Етикетки XP-370B" || a.cfg.Shared[0].Alias != "Етикетки-XP-370B" {
		t.Fatalf("не видано: %+v", a.cfg.Shared)
	}
}

// The point's page has no password, so the code may be entered only for a
// new point or after the server refused the current code.
func TestCodeReplaceable(t *testing.T) {
	a, err := New(t.TempDir(), common.NewLogRing("", 10), &winprint.Fake{})
	if err != nil {
		t.Fatal(err)
	}
	if !a.CodeReplaceable() {
		t.Fatal("нова точка без коду має приймати код")
	}
	a.cfg.Code = "SD1-x"
	if a.CodeReplaceable() {
		t.Fatal("точку з кодом (сервер не відмовляв) не можна переключати зі сторінки")
	}
	a.refused = true
	if !a.CodeReplaceable() {
		t.Fatal("після відмови сервера код має бути можна вписати")
	}
	a.status.Connected = true
	if a.CodeReplaceable() {
		t.Fatal("на зв'язку код не міняють")
	}
}

// «Чек / етикетка» through an unmeasured v4 driver goes through the point's
// own service printer (Generic / Text Only) on the same port; once the shop
// printer is fine again the service printer is removed. Without admin
// rights (non-Windows) the job is refused and waits on the server.
func TestRawFallbackOnV4(t *testing.T) {
	spool := &winprint.Fake{
		Printers: []winprint.PrinterInfo{{Name: "PDF", Driver: "Microsoft Print To PDF", Port: "IP_10.0.0.5_9100"},
			{Name: "XPSW", Driver: "Microsoft XPS Document Writer v4", Port: "USB001"}},
		Versions: map[string]int{"Microsoft Print To PDF": 4, "Microsoft XPS Document Writer v4": 4, "Generic / Text Only": 3},
	}
	a, err := New(t.TempDir(), common.NewLogRing("", 10), spool)
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.Shared = []proto.Printer{{Name: "PDF", Alias: "pdf"}, {Name: "XPSW", Alias: "xpsw"}}
	send := func(id, printer string) proto.JobResult {
		srv, cli := net.Pipe()
		defer srv.Close()
		go a.handleJob(cli)
		go func() {
			proto.WriteLine(srv, proto.JobHeader{ID: id, Printer: printer})
			proto.WriteFrame(srv, []byte("receipt "+id))
			proto.WriteFrame(srv, nil)
		}()
		var res proto.JobResult
		if err := proto.ReadLine(bufio.NewReader(srv), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	// No admin rights: refused (the server keeps the job), nothing printed.
	if res := send("j0", "PDF"); res.OK || !strings.Contains(res.Error, "v4") {
		t.Fatalf("без прав адміністратора мала бути відмова з поясненням: %+v", res)
	}
	a.Admin = &fakeAdmin{spool: spool}
	if res := send("j1", "PDF"); !res.OK {
		t.Fatalf("мало надрукувати через службову чергу: %+v", res)
	}
	if n, m := len(spool.Jobs("PDF")), len(spool.Jobs("SlipDrukka (pdf)")); n != 0 || m != 1 {
		t.Fatalf("на v4: %d, у службову чергу: %d", n, m)
	}
	var comp winprint.PrinterInfo
	for _, p := range spool.Printers {
		if p.Name == "SlipDrukka (pdf)" {
			comp = p
		}
	}
	if comp.Port != "IP_10.0.0.5_9100" || comp.Driver != "Generic / Text Only" {
		t.Fatalf("службова черга не на тому порту чи драйвері: %+v", comp)
	}
	if inv := a.inventory(); len(inv.Printers) != 2 {
		t.Fatalf("службова черга не має потрапляти в перелік принтерів магазину: %d", len(inv.Printers))
	}
	if res := send("j2", "XPSW"); !res.OK || len(spool.Jobs("XPSW")) != 1 {
		t.Fatalf("перевірений v4 мав друкувати сам: %+v", res)
	}
	// The shop printer gets a proper driver -> service printer is removed.
	if err := a.SetDriver("PDF", ""); err != nil {
		t.Fatal(err)
	}
	a.tidyCompanions()
	for _, p := range spool.Printers {
		if p.Name == "SlipDrukka (pdf)" {
			t.Fatal("службову чергу не прибрано")
		}
	}
	if res := send("j3", "PDF"); !res.OK || len(spool.Jobs("PDF")) != 1 {
		t.Fatalf("після заміни драйвера мало друкувати напряму: %+v", res)
	}
	// IP change from the panel keeps tracking quiet (no "port changed").
	a.cfg.Track = map[string]Track{"pdf": {Port: "IP_10.0.0.5_9100", Driver: "Generic / Text Only"}}
	if err := a.SetPrinterIP("PDF", "10.0.0.6", 0); err != nil {
		t.Fatal(err)
	}
	if a.cfg.Track["pdf"].Port != "IP_10.0.0.6_9100" {
		t.Fatalf("трекінг не оновлено: %+v", a.cfg.Track["pdf"])
	}
	if err := a.SetPrinterIP("PDF", "bad host!", 9100); err == nil {
		t.Fatal("некоректну адресу прийнято")
	}
}

// Unsharing a printer forgets its tracking, so a new printer shared later
// under the same alias is not reported as a "driver change".
func TestUnshareForgetsTracking(t *testing.T) {
	spool := &winprint.Fake{Printers: []winprint.PrinterInfo{{Name: "P", Driver: "A", Port: "USB001"}}}
	a, err := New(t.TempDir(), common.NewLogRing("", 10), spool)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetShared([]proto.Printer{{Name: "P", Alias: "x"}}); err != nil {
		t.Fatal(err)
	}
	a.track()
	if _, ok := a.cfg.Track["x"]; !ok {
		t.Fatal("трекінг не записано")
	}
	if err := a.SetShared(nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.cfg.Track["x"]; ok {
		t.Fatal("після зняття з видачі трекінг лишився")
	}
}
