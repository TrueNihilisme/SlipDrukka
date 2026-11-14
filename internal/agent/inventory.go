package agent

// The point keeps the server informed: which printers Windows has, which
// are shared, and what changed. It also watches the shared printers, because
// Windows Update can swap a driver and rename a printer (e.g. "XP-420B" ->
// "XP-420BM") and printing must not silently stop.
//
//   - renamed: the shared name is gone but exactly one other, unshared
//     printer now sits on the same port -> follow it, report "renamed";
//   - driver changed -> report; warn if the new driver is a v4 driver in
//     "чек / етикетка" mode (v4 drivers may not accept RAW data);
//   - gone with no candidate -> report once as an error; jobs wait in the
//     server queue until it is back or the admin fixes it.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"slipdrukka/internal/proto"
	"slipdrukka/internal/winps"
)

// WatchEvery is how often the shared printers are re-checked (tests lower it).
var WatchEvery = 15 * time.Second

const (
	portsMaxAge   = 2 * time.Minute
	maxEvents     = 30
	heartbeatPush = 5 * time.Minute
)

// portCache: port details come from PowerShell, which is slow, so they are
// refreshed only when the printer list changes or every couple of minutes.
type portCache struct {
	mu    sync.Mutex
	at    time.Time
	sig   string
	ports map[string]winps.PortDetail
	usb   []string
}

func (a *Agent) portDetails(sig string) (map[string]winps.PortDetail, []string) {
	a.ports.mu.Lock()
	defer a.ports.mu.Unlock()
	if a.Admin != nil && (a.ports.ports == nil || a.ports.sig != sig || time.Since(a.ports.at) > portsMaxAge) {
		if list, usb, err := a.Admin.Ports(); err == nil {
			m := map[string]winps.PortDetail{}
			for _, p := range list {
				m[p.Name] = p
			}
			a.ports.ports, a.ports.usb, a.ports.at, a.ports.sig = m, usb, time.Now(), sig
		}
	}
	return a.ports.ports, a.ports.usb
}

// inventory builds the report sent to the server.
func (a *Agent) inventory() *proto.Inventory {
	inv := &proto.Inventory{}
	list, _ := a.spool.List()
	versions, _ := a.spool.DriverVersions()
	var sig strings.Builder
	for _, p := range list {
		fmt.Fprintf(&sig, "%s|%s|%s;", p.Name, p.Driver, p.Port)
	}
	ports, usb := a.portDetails(sig.String())
	for _, p := range list {
		if isCompanion(p.Name) {
			continue // our own service printer (fallback.go), not the shop's
		}
		ip := proto.InvPrinter{Name: p.Name, Driver: p.Driver, DriverVersion: versions[p.Driver], Port: p.Port, Status: p.Status, Jobs: p.Jobs}
		if d, ok := ports[p.Port]; ok && d.Host != "" {
			ip.PortHost, ip.PortNumber = d.Host, d.Number
		}
		inv.Printers = append(inv.Printers, ip)
	}
	for name, d := range ports {
		if strings.HasPrefix(strings.ToUpper(name), "USB") {
			inv.USBPorts = append(inv.USBPorts, proto.USBPort{Name: name, Description: d.Description})
		}
	}
	sort.Slice(inv.USBPorts, func(i, j int) bool { return inv.USBPorts[i].Name < inv.USBPorts[j].Name })
	inv.USBSeen = usb
	a.mu.Lock()
	inv.Shared = append([]proto.Printer(nil), a.cfg.Shared...)
	inv.Events = append([]proto.Event(nil), a.events...)
	a.mu.Unlock()
	inv.Net = a.netInfo()
	return inv
}

func (a *Agent) addEvent(level, kind, printer, text string) {
	ev := proto.Event{Time: time.Now(), Level: level, Kind: kind, Printer: printer, Text: text}
	a.mu.Lock()
	a.events = append(a.events, ev)
	if len(a.events) > maxEvents {
		a.events = a.events[len(a.events)-maxEvents:]
	}
	a.mu.Unlock()
	a.Log.Printf("%s", text)
}

// track applies the rename / driver / missing rules. It returns true when
// the shared configuration changed.
func (a *Agent) track() bool {
	list, err := a.spool.List()
	if err != nil {
		return false
	}
	versions, _ := a.spool.DriverVersions()
	byName := map[string]int{}
	for i, p := range list {
		byName[p.Name] = i
	}

	type note struct{ level, kind, printer, text string }
	var notes []note
	changed := false

	a.mu.Lock()
	// Forget printers we created that are no longer in Windows.
	if len(a.cfg.Created) > 0 {
		keep := a.cfg.Created[:0]
		for _, n := range a.cfg.Created {
			if _, ok := byName[n]; ok {
				keep = append(keep, n)
			}
		}
		if len(keep) != len(a.cfg.Created) {
			a.cfg.Created = keep
			changed = true
		}
	}
	sharedNames := map[string]bool{}
	for _, p := range a.cfg.Shared {
		sharedNames[p.Name] = true
	}
	for i := range a.cfg.Shared {
		p := &a.cfg.Shared[i]
		t := a.cfg.Track[p.Alias]
		if idx, ok := byName[p.Name]; ok {
			cur := list[idx]
			if t.Port == "" && t.Driver == "" { // first sight: just remember
				a.cfg.Track[p.Alias] = Track{Port: cur.Port, Driver: cur.Driver}
				changed = true
				continue
			}
			if a.missing[p.Alias] {
				delete(a.missing, p.Alias)
				notes = append(notes, note{"info", "restored", p.Name, fmt.Sprintf("Принтер «%s» знову на місці", p.Name)})
			}
			if t.Driver != cur.Driver {
				text := fmt.Sprintf("Драйвер принтера «%s» змінено: «%s» → «%s»", p.Name, t.Driver, cur.Driver)
				level := "info"
				if p.Mode == proto.ModeRaw && driverUnsafe(cur.Driver, versions) {
					level = "warn"
					text += ". Це драйвер v4, він може мовчки не пропускати чеки, тому чеки підуть через службову чергу Generic / Text Only на тому ж порту. Робити нічого не треба"
				}
				notes = append(notes, note{level, "driver_changed", p.Name, text})
				t.Driver = cur.Driver
				changed = true
			}
			if t.Port != cur.Port {
				notes = append(notes, note{"info", "port_changed", p.Name, fmt.Sprintf("Порт принтера «%s» змінено: %s → %s", p.Name, t.Port, cur.Port)})
				t.Port = cur.Port
				changed = true
			}
			a.cfg.Track[p.Alias] = t
			continue
		}
		// The shared printer is gone. Same port, exactly one unshared printer
		// there -> Windows renamed it.
		var cand []string
		if t.Port != "" {
			for _, q := range list {
				if q.Port == t.Port && !sharedNames[q.Name] && !isCompanion(q.Name) {
					cand = append(cand, q.Name)
				}
			}
		}
		if len(cand) == 1 {
			old := p.Name
			p.Name = cand[0]
			sharedNames[p.Name] = true
			delete(a.missing, p.Alias)
			cur := list[byName[p.Name]]
			notes = append(notes, note{"warn", "renamed", p.Name,
				fmt.Sprintf("Windows перейменував принтер «%s» → «%s» (той самий порт %s) — підхоплено автоматично", old, p.Name, t.Port)})
			if cur.Driver != t.Driver {
				notes = append(notes, note{"info", "driver_changed", p.Name, fmt.Sprintf("Драйвер принтера «%s» змінено: «%s» → «%s»", p.Name, t.Driver, cur.Driver)})
			}
			a.cfg.Track[p.Alias] = Track{Port: cur.Port, Driver: cur.Driver}
			changed = true
			continue
		}
		if !a.missing[p.Alias] {
			a.missing[p.Alias] = true
			why := "його немає у Windows"
			if len(cand) > 1 {
				why = fmt.Sprintf("на його порту %s тепер кілька принтерів (%s)", t.Port, strings.Join(cand, ", "))
			}
			if p.Mode == proto.ModeRaw && t.Port != "" {
				notes = append(notes, note{"warn", "missing", p.Name, fmt.Sprintf("Принтер «%s» зник: %s. Чеки друкуються через службову чергу на його порту %s — робити нічого не треба", p.Name, why, t.Port)})
			} else {
				notes = append(notes, note{"error", "missing", p.Name, fmt.Sprintf("Принтер «%s» зник: %s. Документи чекатимуть у черзі сервера — виберіть принтер у панелі сервера", p.Name, why)})
			}
		}
	}
	if changed {
		a.saveLocked()
	}
	a.mu.Unlock()
	for _, n := range notes {
		a.addEvent(n.level, n.kind, n.printer, n.text)
	}
	return changed || len(notes) > 0
}

// watch re-checks the printers periodically or when nudged, and pushes the
// inventory to the server when anything changed (or as a heartbeat).
func (a *Agent) watch(ctx context.Context) {
	tick := time.NewTicker(WatchEvery)
	defer tick.Stop()
	var lastSig string
	var lastPush time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-a.dirty:
		}
		a.track()
		a.tidyCompanions()
		inv := a.inventory()
		b, _ := json.Marshal(inv)
		sig := string(b)
		if sig != lastSig || time.Since(lastPush) > heartbeatPush {
			if a.push(inv) {
				lastSig, lastPush = sig, time.Now()
			}
		}
	}
}

// push sends the inventory to the server over the current session.
func (a *Agent) push(inv *proto.Inventory) bool {
	a.mu.Lock()
	sess := a.sess
	a.mu.Unlock()
	if sess == nil {
		return false
	}
	st, err := sess.Open()
	if err != nil {
		return false
	}
	defer st.Close()
	st.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if proto.WriteLine(st, proto.JobHeader{Kind: proto.KindInventory}) != nil {
		return false
	}
	return proto.WriteLine(st, inv) == nil
}
