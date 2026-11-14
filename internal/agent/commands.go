package agent

// Commands from the server. The point has no settings page of its own any
// more: the admin manages it from the server panel, and these commands
// arrive over the same authenticated tunnel the point opened.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/winps"
)

// Admin manages printers in Windows on the point.
type Admin interface {
	Drivers() ([]string, error)
	AddNetworkPrinter(name, host string, port int, driver string) error
	AddPrinterOnPort(name, port, driver string) error
	RenamePrinter(old, new string) error
	SetDriver(name, driver string) error
	SetPrinterIP(name, host string, port int) error
	RemovePrinter(name string) error
	Ports() ([]winps.PortDetail, []string, error)
}

// WinAdmin is the real Windows implementation of Admin.
type WinAdmin struct{}

func (WinAdmin) Drivers() ([]string, error) { return winps.Drivers() }
func (WinAdmin) AddNetworkPrinter(name, host string, port int, driver string) error {
	return winps.AddNetworkPrinter(name, host, port, driver)
}
func (WinAdmin) AddPrinterOnPort(name, port, driver string) error {
	return winps.AddPrinterOnPort(name, port, driver)
}
func (WinAdmin) RenamePrinter(old, new string) error { return winps.RenamePrinter(old, new) }
func (WinAdmin) SetDriver(name, driver string) error { return winps.SetPrinterDriver(name, driver) }
func (WinAdmin) SetPrinterIP(name, host string, port int) error {
	return winps.SetPrinterIP(name, host, port)
}
func (WinAdmin) RemovePrinter(name string) error              { return winps.RemovePrinter(name) }
func (WinAdmin) Ports() ([]winps.PortDetail, []string, error) { return winps.Ports() }

var (
	hostRe  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,250}[A-Za-z0-9])?$`)
	labelRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,62}$`)
	portRe  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
)

// checkPrinterHost accepts only addresses on the shop's own network.
//
// The address comes from the server. If the server or its admin page ever
// falls into the wrong hands, an unrestricted address would let them point a
// shop printer at a host on the internet and receive everything the shop
// prints, or use the point to send data to any host it can reach. A shop
// printer is always local, so nothing is lost by refusing the rest:
//
//   - an IP address must be private (10.x, 172.16-31.x, 192.168.x, fc00::/7),
//     link-local or this computer itself;
//   - a name must be a single label starting with a letter (resolved on the
//     local network; a bare number could be read as an IP address) or end
//     in a suffix that is never public.
func checkPrinterHost(host string) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLoopback() {
			return nil
		}
		return errors.New("адреса принтера має бути в локальній мережі магазину (10.x, 172.16–31.x, 192.168.x)")
	}
	if !hostRe.MatchString(host) {
		return errors.New("IP-адреса або ім'я принтера некоректні")
	}
	lower := strings.ToLower(host)
	if !strings.Contains(lower, ".") {
		if labelRe.MatchString(host) {
			return nil
		}
		return errors.New("IP-адреса або ім'я принтера некоректні")
	}
	for _, suffix := range []string{".local", ".lan", ".home.arpa", ".internal"} {
		if strings.HasSuffix(lower, suffix) {
			return nil
		}
	}
	return errors.New("ім'я принтера має бути з локальної мережі магазину (без крапок або на .local, .lan); для інших принтерів вкажіть IP-адресу")
}

// ours reports whether the server may change the Windows printer name: only
// printers the point offers to the server and printers this program created
// on the server's command. The rest of the shop's printers are none of the
// server's business, even though the service account could change them.
func (a *Agent) ours(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.cfg.Shared {
		if p.Name == name {
			return true
		}
	}
	for _, n := range a.cfg.Created {
		if n == name {
			return true
		}
	}
	return false
}

func errNotOurs(name string) error {
	return fmt.Errorf("принтер «%s» не видано на сервер і не створено цією програмою — змінювати його з панелі не можна", name)
}

// noteCreated remembers a printer this program created.
func (a *Agent) noteCreated(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, n := range a.cfg.Created {
		if n == name {
			return
		}
	}
	a.cfg.Created = append(a.cfg.Created, name)
	a.saveLocked()
}

func checkPrinterName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 || strings.ContainsAny(name, `\,`) {
		return "", errors.New("назва принтера: до 100 символів, без «\\» і коми")
	}
	return name, nil
}

func checkMode(m string) error {
	if m != proto.ModeRaw && m != proto.ModeXPS {
		return fmt.Errorf("невідомий режим «%s»", m)
	}
	return nil
}

// NetPrinter: a printer on the shop network (IP:port, RAW).
type NetPrinter struct {
	Name   string `json:"name"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Driver string `json:"driver"`
	Share  bool   `json:"share"`
	Alias  string `json:"alias"`
	Mode   string `json:"mode"`
}

// PortPrinter: a printer on an existing port of the point (e.g. USB002).
type PortPrinter struct {
	Name   string `json:"name"`
	Port   string `json:"port"`
	Driver string `json:"driver"`
	Share  bool   `json:"share"`
	Alias  string `json:"alias"`
	Mode   string `json:"mode"`
}

func (a *Agent) needAdmin() error {
	if a.Admin == nil {
		return errors.New("керування принтерами доступне лише у Windows")
	}
	return nil
}

func (a *Agent) share(name, alias, mode string) error {
	a.mu.Lock()
	list := append([]proto.Printer(nil), a.cfg.Shared...)
	a.mu.Unlock()
	if strings.TrimSpace(alias) == "" {
		alias = name
	}
	list = append(list, proto.Printer{Name: name, Alias: alias, Mode: mode})
	return a.SetShared(list)
}

// AddNetworkPrinter installs a network printer into Windows and, if asked,
// offers it to the server.
func (a *Agent) AddNetworkPrinter(np NetPrinter) error {
	if err := a.needAdmin(); err != nil {
		return err
	}
	var err error
	if np.Name, err = checkPrinterName(np.Name); err != nil {
		return err
	}
	np.Host = strings.TrimSpace(np.Host)
	if err := checkPrinterHost(np.Host); err != nil {
		return err
	}
	if np.Port == 0 {
		np.Port = 9100
	}
	if err := common.CheckPort(np.Port, "порт принтера"); err != nil {
		return err
	}
	if np.Driver == "" {
		np.Driver = winps.GenericDriver
	}
	if err := checkMode(np.Mode); err != nil {
		return err
	}
	if err := a.Admin.AddNetworkPrinter(np.Name, np.Host, np.Port, np.Driver); err != nil {
		return err
	}
	a.noteCreated(np.Name)
	a.addEvent("info", "added", np.Name, fmt.Sprintf("Створено принтер «%s» → %s:%d (драйвер «%s»)", np.Name, np.Host, np.Port, np.Driver))
	if np.Share {
		return a.share(np.Name, np.Alias, np.Mode)
	}
	a.changed()
	return nil
}

// AddPortPrinter creates a printer on an existing port (USB00x) with the
// built-in Generic / Text Only driver by default: in "чек / етикетка" mode
// the driver on the server renders, so nothing else is needed here.
func (a *Agent) AddPortPrinter(pp PortPrinter) error {
	if err := a.needAdmin(); err != nil {
		return err
	}
	var err error
	if pp.Name, err = checkPrinterName(pp.Name); err != nil {
		return err
	}
	if !portRe.MatchString(pp.Port) {
		return errors.New("некоректна назва порту")
	}
	if pp.Driver == "" {
		pp.Driver = winps.GenericDriver
	}
	if err := checkMode(pp.Mode); err != nil {
		return err
	}
	if err := a.Admin.AddPrinterOnPort(pp.Name, pp.Port, pp.Driver); err != nil {
		return err
	}
	a.noteCreated(pp.Name)
	a.addEvent("info", "added", pp.Name, fmt.Sprintf("Створено принтер «%s» на порту %s (драйвер «%s»)", pp.Name, pp.Port, pp.Driver))
	if pp.Share {
		return a.share(pp.Name, pp.Alias, pp.Mode)
	}
	a.changed()
	return nil
}

// RenamePrinter renames a Windows printer on the point and keeps sharing it.
func (a *Agent) RenamePrinter(old, newName string) error {
	if err := a.needAdmin(); err != nil {
		return err
	}
	var err error
	if newName, err = checkPrinterName(newName); err != nil {
		return err
	}
	if !a.ours(old) {
		return errNotOurs(old)
	}
	if err := a.Admin.RenamePrinter(old, newName); err != nil {
		return err
	}
	a.mu.Lock()
	for i := range a.cfg.Shared {
		if a.cfg.Shared[i].Name == old {
			a.cfg.Shared[i].Name = newName
		}
	}
	for i := range a.cfg.Created {
		if a.cfg.Created[i] == old {
			a.cfg.Created[i] = newName
		}
	}
	a.saveLocked()
	a.mu.Unlock()
	a.addEvent("info", "renamed_by_admin", newName, fmt.Sprintf("Принтер «%s» перейменовано на «%s» з панелі сервера", old, newName))
	a.changed()
	return nil
}

// SetDriver switches a printer on the point to another driver ("" = the
// built-in Generic / Text Only). Used from the panel when a driver swallows
// receipt data (the point otherwise prints through a service printer, see
// fallback.go).
func (a *Agent) SetDriver(name, driver string) error {
	if err := a.needAdmin(); err != nil {
		return err
	}
	if driver == "" {
		driver = winps.GenericDriver
	}
	if !a.ours(name) {
		return errNotOurs(name)
	}
	list, err := a.spool.List()
	if err != nil {
		return err
	}
	old := ""
	found := false
	for _, p := range list {
		if p.Name == name {
			found, old = true, p.Driver
		}
	}
	if !found {
		return fmt.Errorf("принтера «%s» немає у Windows точки", name)
	}
	if err := a.Admin.SetDriver(name, driver); err != nil {
		return err
	}
	a.mu.Lock()
	for _, p := range a.cfg.Shared {
		if p.Name == name {
			t := a.cfg.Track[p.Alias]
			t.Driver = driver // our own change: no "driver changed" event
			a.cfg.Track[p.Alias] = t
		}
	}
	a.saveLocked()
	a.mu.Unlock()
	a.addEvent("info", "driver_set", name, fmt.Sprintf("Драйвер принтера «%s» замінено з панелі сервера: «%s» → «%s»", name, old, driver))
	a.changed()
	return nil
}

// SetPrinterIP points a network printer of the point at a new address (the
// printer keeps its name, so the server port and the programs printing to
// it stay the same).
func (a *Agent) SetPrinterIP(name, host string, port int) error {
	if err := a.needAdmin(); err != nil {
		return err
	}
	host = strings.TrimSpace(host)
	if err := checkPrinterHost(host); err != nil {
		return err
	}
	if port == 0 {
		port = 9100
	}
	if err := common.CheckPort(port, "порт принтера"); err != nil {
		return err
	}
	if !a.ours(name) {
		return errNotOurs(name)
	}
	list, err := a.spool.List()
	if err != nil {
		return err
	}
	old := ""
	found := false
	for _, p := range list {
		if p.Name == name {
			found, old = true, p.Port
		}
	}
	if !found {
		return fmt.Errorf("принтера «%s» немає у Windows точки", name)
	}
	if err := a.Admin.SetPrinterIP(name, host, port); err != nil {
		return err
	}
	newPort := fmt.Sprintf("IP_%s_%d", host, port)
	a.mu.Lock()
	for _, p := range a.cfg.Shared {
		if p.Name == name {
			t := a.cfg.Track[p.Alias]
			t.Port = newPort // our own change: no "port changed" event
			a.cfg.Track[p.Alias] = t
		}
	}
	a.saveLocked()
	a.mu.Unlock()
	a.addEvent("info", "ip_set", name, fmt.Sprintf("Адресу принтера «%s» змінено з панелі сервера: %s → %s:%d", name, old, host, port))
	a.changed()
	return nil
}

// TestPort prints a short text straight to a port, so someone at the shop
// can see which physical printer sits there.
func (a *Agent) TestPort(port string) error {
	if err := a.needAdmin(); err != nil {
		return err
	}
	if !portRe.MatchString(port) {
		return errors.New("некоректна назва порту")
	}
	tmp := "SlipDrukka test " + port
	_ = a.Admin.RemovePrinter(tmp) // leftover from an interrupted test
	if err := a.Admin.AddPrinterOnPort(tmp, port, winps.GenericDriver); err != nil {
		return err
	}
	defer a.Admin.RemovePrinter(tmp)
	job, err := a.spool.Open(tmp, "SlipDrukka port test")
	if err != nil {
		return err
	}
	text := fmt.Sprintf("\r\n SlipDrukka\r\n Тестовий друк\r\n Порт: %s\r\n %s\r\n\r\n\r\n\r\n", port, time.Now().Format("02.01.2006 15:04"))
	if _, err := job.Write([]byte(text)); err != nil {
		job.Abort()
		return err
	}
	if err := job.Close(); err != nil {
		return err
	}
	// Give the spooler time to hand the job to the port before the
	// temporary printer is removed.
	for i := 0; i < 30; i++ {
		busy := false
		if list, err := a.spool.List(); err == nil {
			for _, p := range list {
				if p.Name == tmp && p.Jobs > 0 {
					busy = true
				}
			}
		}
		if !busy {
			break
		}
		time.Sleep(time.Second)
	}
	a.addEvent("info", "test_port", port, fmt.Sprintf("Тестовий друк на порт %s надіслано", port))
	return nil
}

// handleCmd executes one command and answers with a CmdResult.
func (a *Agent) handleCmd(st net.Conn, hdr proto.JobHeader) {
	data, err := a.runCmd(hdr.Cmd, hdr.Args)
	res := proto.CmdResult{OK: err == nil, Data: data}
	if err != nil {
		res.Error = err.Error()
		a.Log.Printf("команда «%s» від сервера: %v", hdr.Cmd, err)
	} else {
		a.Log.Printf("команда «%s» від сервера виконана", hdr.Cmd)
	}
	st.SetWriteDeadline(time.Now().Add(30 * time.Second))
	proto.WriteLine(st, res)
}

func (a *Agent) runCmd(cmd string, args json.RawMessage) (json.RawMessage, error) {
	decode := func(v any) error {
		if err := json.Unmarshal(args, v); err != nil {
			return errors.New("некоректні параметри команди")
		}
		return nil
	}
	switch cmd {
	case "set_shared":
		var in struct{ Printers []proto.Printer }
		if err := decode(&in); err != nil {
			return nil, err
		}
		return nil, a.SetShared(in.Printers)
	case "add_net_printer":
		var in NetPrinter
		if err := decode(&in); err != nil {
			return nil, err
		}
		return nil, a.AddNetworkPrinter(in)
	case "add_port_printer":
		var in PortPrinter
		if err := decode(&in); err != nil {
			return nil, err
		}
		return nil, a.AddPortPrinter(in)
	case "rename_printer":
		var in struct{ Old, New string }
		if err := decode(&in); err != nil {
			return nil, err
		}
		return nil, a.RenamePrinter(in.Old, in.New)
	case "set_driver":
		var in struct{ Name, Driver string }
		if err := decode(&in); err != nil {
			return nil, err
		}
		return nil, a.SetDriver(in.Name, in.Driver)
	case "set_printer_ip":
		var in struct {
			Name, Host string
			Port       int
		}
		if err := decode(&in); err != nil {
			return nil, err
		}
		return nil, a.SetPrinterIP(in.Name, in.Host, in.Port)
	case "test_port":
		var in struct{ Port string }
		if err := decode(&in); err != nil {
			return nil, err
		}
		return nil, a.TestPort(in.Port)
	case "drivers":
		if err := a.needAdmin(); err != nil {
			return nil, err
		}
		list, err := a.Admin.Drivers()
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(list)
		return b, nil
	}
	return nil, fmt.Errorf("невідома команда «%s»", cmd)
}
