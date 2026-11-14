package agent

// Autonomy: a shop keeps printing without anyone opening the panel.
//
// In «чек / етикетка» mode the driver on the point draws nothing (the data
// is ready-made on the server), so when the shop's own printer object can't
// be trusted or found — a v4 driver that may swallow the data, or the
// printer was deleted / became ambiguous after an update — the point prints
// through its own service printer «SlipDrukka (<alias>)» with the built-in
// "Generic / Text Only" driver on the same port. The shop's printer itself
// is never touched (staff may print on it locally). The service printer is
// removed again once the shop's printer is usable.

import (
	"fmt"
	"strings"

	"slipdrukka/internal/proto"
	"slipdrukka/internal/winprint"
	"slipdrukka/internal/winps"
)

const companionPrefix = "SlipDrukka ("

func companionName(alias string) string { return companionPrefix + alias + ")" }
func isCompanion(name string) bool      { return strings.HasPrefix(name, companionPrefix) }

func driverUnsafe(driver string, versions map[string]int) bool {
	return versions[driver] == 4 && !rawSafeV4[driver]
}

// rawTarget picks the Windows printer for a «чек / етикетка» job: the shop's
// printer when it is usable, otherwise the service printer on its port.
func (a *Agent) rawTarget(name, alias string) (string, error) {
	list, err := a.spool.List()
	if err != nil {
		return name, nil // can't tell; try the printer itself
	}
	versions, _ := a.spool.DriverVersions()
	cn := companionName(alias)
	var shop, comp *winprint.PrinterInfo
	for i := range list {
		switch list[i].Name {
		case name:
			shop = &list[i]
		case cn:
			comp = &list[i]
		}
	}
	var reason, port string
	switch {
	case shop == nil:
		reason = "його немає у Windows"
		a.mu.Lock()
		port = a.cfg.Track[alias].Port
		a.mu.Unlock()
	case driverUnsafe(shop.Driver, versions):
		reason = fmt.Sprintf("драйвер «%s» (v4) може мовчки не пропускати чеки", shop.Driver)
		port = shop.Port
	default:
		return name, nil
	}
	if port == "" {
		return "", fmt.Errorf("принтер «%s»: %s, а його порт невідомий — виберіть принтер у панелі сервера", name, reason)
	}
	if a.Admin == nil {
		return "", fmt.Errorf("принтер «%s»: %s; службова черга доступна лише у Windows", name, reason)
	}
	if comp != nil && (comp.Port != port || comp.Driver != winps.GenericDriver) {
		if err := a.Admin.RemovePrinter(cn); err != nil {
			return "", fmt.Errorf("принтер «%s»: %s; службову чергу «%s» не вдалося перестворити: %v", name, reason, cn, err)
		}
		comp = nil
	}
	if comp == nil {
		if err := a.Admin.AddPrinterOnPort(cn, port, winps.GenericDriver); err != nil {
			return "", fmt.Errorf("принтер «%s»: %s; службову чергу на порту %s створити не вдалося: %v", name, reason, port, err)
		}
	}
	key := alias + "|" + reason
	a.mu.Lock()
	first := !a.fallbackNoted[key]
	a.fallbackNoted[key] = true
	a.mu.Unlock()
	if first {
		a.addEvent("warn", "fallback", name, fmt.Sprintf("Принтер «%s»: %s — чеки друкуються через службову чергу «%s» (Generic / Text Only) на тому ж порту %s. Робити нічого не треба", name, reason, cn, port))
	}
	return cn, nil
}

// tidyCompanions removes service printers that are no longer needed: the
// printer is no longer shared in «чек / етикетка» mode, or the shop's own
// printer is usable again.
func (a *Agent) tidyCompanions() {
	if a.Admin == nil {
		return
	}
	list, err := a.spool.List()
	if err != nil {
		return
	}
	versions, _ := a.spool.DriverVersions()
	present := map[string]winprint.PrinterInfo{}
	for _, p := range list {
		present[p.Name] = p
	}
	need := map[string]bool{}
	a.mu.Lock()
	for _, p := range a.cfg.Shared {
		if p.Mode != proto.ModeRaw {
			continue
		}
		if shop, ok := present[p.Name]; !ok || driverUnsafe(shop.Driver, versions) {
			need[companionName(p.Alias)] = true
		}
	}
	a.mu.Unlock()
	for _, p := range list {
		if !isCompanion(p.Name) || need[p.Name] || p.Jobs > 0 {
			continue
		}
		if err := a.Admin.RemovePrinter(p.Name); err != nil {
			a.Log.Printf("службова черга «%s»: прибрати не вдалося: %v", p.Name, err)
			continue
		}
		alias := strings.TrimSuffix(strings.TrimPrefix(p.Name, companionPrefix), ")")
		a.mu.Lock()
		for k := range a.fallbackNoted {
			if strings.HasPrefix(k, alias+"|") {
				delete(a.fallbackNoted, k)
			}
		}
		a.mu.Unlock()
		a.addEvent("info", "fallback_removed", p.Name, fmt.Sprintf("Службову чергу «%s» прибрано: принтер магазину знову придатний", p.Name))
	}
}
