// Package nettools answers "where is this shop on the network": its own
// addresses, its router, and the route to the other side (traceroute).
// Nothing here asks an outside service; the public address of a shop is what
// the server sees, and the server tells the shop.
package nettools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"slipdrukka/internal/winsys"
)

// Iface is one network adapter with its addresses.
type Iface struct {
	Name  string   `json:"name"`
	Addrs []string `json:"addrs"`
}

// LocalAddrs lists the adapters that are up, without loopback and
// link-local addresses.
func LocalAddrs() []Iface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []Iface
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		var list []string
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			list = append(list, ipn.IP.String())
		}
		if len(list) > 0 {
			out = append(out, Iface{Name: i.Name, Addrs: list})
		}
	}
	return out
}

// Hop is one line of a traceroute: the router's address ("" when it did not
// answer) and up to three round trips in milliseconds (-1 = no answer).
type Hop struct {
	N  int       `json:"n"`
	IP string    `json:"ip"`
	MS []float64 `json:"ms"`
}

var traceMu sync.Mutex

// ErrBusy: a traceroute is already running in this program.
var ErrBusy = errors.New("трасування вже йде — зачекайте")

// Trace runs the system traceroute to host (at most 20 hops) and returns the
// hops. One trace at a time per program.
func Trace(ctx context.Context, host string) ([]Hop, error) {
	if host == "" || strings.HasPrefix(host, "-") {
		return nil, errors.New("немає адреси для трасування")
	}
	if !traceMu.TryLock() {
		return nil, ErrBusy
	}
	defer traceMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// -d: numbers only (no name lookups), -h: hops, -w: ms per answer.
		cmd = exec.CommandContext(ctx, winsys.Exe("tracert.exe"), "-d", "-h", "20", "-w", "700", host)
	} else {
		cmd = exec.CommandContext(ctx, "traceroute", "-n", "-m", "20", "-w", "1", "-q", "3", host)
	}
	out, err := cmd.Output()
	hops := ParseTrace(out)
	if len(hops) == 0 {
		if err == nil {
			err = errors.New("трасування нічого не повернуло")
		}
		return nil, err
	}
	return hops, nil
}

var (
	reHopStart = regexp.MustCompile(`^\s*(\d+)\s+(.*)$`)
	reIPv4     = regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)
	reIPv6     = regexp.MustCompile(`\b([0-9a-fA-F]{0,4}(?::[0-9a-fA-F]{0,4}){2,7})\b`)
	reTime     = regexp.MustCompile(`\*|<?(\d+(?:[.,]\d+)?)`)
)

// ParseTrace reads tracert (Windows, any display language) or traceroute -n
// output. Only digits, "*" and addresses are read, so the words around them
// may be in any language or code page.
func ParseTrace(out []byte) []Hop {
	var hops []Hop
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		m := reHopStart.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		rest := m[2]
		h := Hop{N: n, MS: []float64{}}
		if ip := reIPv4.FindString(rest); ip != "" {
			h.IP = ip
		} else if ip := reIPv6.FindString(rest); ip != "" && strings.Count(ip, ":") >= 2 {
			h.IP = ip
		}
		if h.IP != "" {
			rest = strings.Replace(rest, h.IP, " ", 1)
		}
		for _, t := range reTime.FindAllStringSubmatch(rest, -1) {
			if len(h.MS) == 3 {
				break
			}
			if t[0] == "*" {
				h.MS = append(h.MS, -1)
				continue
			}
			v, _ := strconv.ParseFloat(strings.Replace(t[1], ",", ".", 1), 64)
			if strings.HasPrefix(t[0], "<") {
				v /= 2 // "<1 ms": shown as "<1"
			}
			h.MS = append(h.MS, v)
		}
		if n > 0 && n <= 64 && (h.IP != "" || len(h.MS) > 0) {
			hops = append(hops, h)
		}
	}
	return hops
}
