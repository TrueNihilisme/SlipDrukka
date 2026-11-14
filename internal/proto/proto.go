// Package proto holds everything the server and the agent must agree on:
// the connection code, the handshake messages and the job framing.
//
// Wire layout of one agent connection:
//
//	TLS (server cert pinned by fingerprint from the connection code)
//	agent -> server : Hello  (one JSON line)
//	server -> agent : Welcome (one JSON line)
//	then a yamux session; the server opens one stream per print job:
//	server -> agent : JobHeader (one JSON line)
//	server -> agent : frames [uint32 big-endian length][bytes]..., length 0 = end
//	agent  -> server: JobResult (one JSON line)
package proto

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// Version is the protocol version. Bump on incompatible changes.
	// 2: inventory push from the agent, commands from the server.
	Version = 2
	// MaxFrame is the largest data frame either side will accept.
	MaxFrame = 64 * 1024
	// MaxLine is the largest JSON line either side will accept.
	MaxLine    = 64 * 1024
	codePrefix = "SD1-"
)

// Code is what the server admin copies into an agent. It tells the agent
// where to connect, which key to present and which certificate to trust.
type Code struct {
	Addr        string `json:"a"`
	Key         string `json:"k"`
	Fingerprint string `json:"f"`
	// WireGuard: the server's public key (base64), when the server offers
	// the WireGuard channel. Older programs ignore it.
	WireGuard string `json:"w,omitempty"`
}

func (c Code) String() string {
	b, _ := json.Marshal(c)
	return codePrefix + base64.RawURLEncoding.EncodeToString(b)
}

// ParseCode accepts a code with surrounding whitespace.
func ParseCode(s string) (Code, error) {
	var c Code
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, codePrefix) {
		return c, errors.New("код має починатися з " + codePrefix)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, codePrefix))
	if err != nil {
		return c, errors.New("код пошкоджений")
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, errors.New("код пошкоджений")
	}
	if c.Addr == "" || c.Key == "" || c.Fingerprint == "" {
		return c, errors.New("у коді бракує даних")
	}
	return c, nil
}

// Printer modes. Raw passes driver output from the server untouched (receipt
// and label printers). Document mode carries an XPS document produced on the
// server by "Microsoft XPS Document Writer"; the point prints it through its
// own driver, so printers that need their local driver (firmware upload,
// two-way talk) work too.
const (
	ModeRaw = ""
	ModeXPS = "xps"
)

// Printer is one printer the agent offers to the server.
type Printer struct {
	Name  string `json:"name"`           // Windows printer name on the agent PC
	Alias string `json:"alias"`          // short name shown on the server
	Mode  string `json:"mode,omitempty"` // ModeRaw or ModeXPS
}

type Hello struct {
	Proto     int        `json:"proto"`
	Version   string     `json:"version"`
	Key       string     `json:"key"`
	Hostname  string     `json:"hostname"`
	Printers  []Printer  `json:"printers"`
	Inventory *Inventory `json:"inventory,omitempty"`
}

// Inventory is what the point reports about itself: every Windows printer,
// the USB printer ports, what is shared and recent events (renames etc.).
// The agent pushes it on connect and whenever something changes.
type Inventory struct {
	Printers []InvPrinter `json:"printers"`
	USBPorts []USBPort    `json:"usb_ports"`
	USBSeen  []string     `json:"usb_seen,omitempty"` // models Windows reports as connected
	Shared   []Printer    `json:"shared"`
	Events   []Event      `json:"events"`
}

type InvPrinter struct {
	Name          string `json:"name"`
	Driver        string `json:"driver"`
	DriverVersion int    `json:"driver_version,omitempty"` // 3 = classic, 4 = v4 (XPS-based)
	Port          string `json:"port"`
	PortHost      string `json:"port_host,omitempty"` // TCP/IP ports
	PortNumber    int    `json:"port_number,omitempty"`
	Status        uint32 `json:"status"`
	Jobs          uint32 `json:"jobs"`
}

type USBPort struct {
	Name        string `json:"name"` // USB001
	Description string `json:"description"`
}

// Event is something the admin should know about a point's printers.
type Event struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"` // info, warn, error
	Kind    string    `json:"kind"`  // renamed, driver_changed, missing, restored, ...
	Printer string    `json:"printer"`
	Text    string    `json:"text"`
}

// Stream kinds. Server -> agent: "" (print job) or "cmd". Agent -> server:
// "inventory".
const (
	KindJob       = ""
	KindCmd       = "cmd"
	KindInventory = "inventory"
)

// CmdResult answers a KindCmd stream.
type CmdResult struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

type Welcome struct {
	OK    bool   `json:"ok"`
	Name  string `json:"name,omitempty"`
	Error string `json:"error,omitempty"`
	// WireGuard: the server's public key when it offers the WireGuard
	// channel ("" = it does not); the point uses it on the next connection.
	WireGuard string `json:"wg,omitempty"`
	// Via: how this connection reached the server ("wireguard" or "tcp").
	Via string `json:"via,omitempty"`
	// Lang: the language the server writes its log in; the point writes
	// its own log in the same one ("uk" or "en").
	Lang string `json:"lang,omitempty"`
}

// Channels a point can use.
const (
	ViaTCP       = "tcp"
	ViaWireGuard = "wireguard"
)

// JobHeader opens every stream. Kind selects what follows: a print job
// (frames), a command (Cmd + Args, answered with CmdResult) or an agent push.
type JobHeader struct {
	Kind    string          `json:"kind,omitempty"`
	ID      string          `json:"id,omitempty"`
	Printer string          `json:"printer,omitempty"` // Windows printer name on the agent PC
	Cmd     string          `json:"cmd,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
}

type JobResult struct {
	OK    bool   `json:"ok"`
	Bytes int64  `json:"bytes"`
	Error string `json:"error,omitempty"`
}

// WriteLine writes v as one JSON line.
func WriteLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// ReadLine reads one JSON line byte by byte, so nothing past the newline is
// consumed. That matters during the handshake, where the same connection is
// handed to yamux right after.
func ReadLine(r io.Reader, v any) error {
	var buf []byte
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, one); err != nil {
			return err
		}
		if one[0] == '\n' {
			break
		}
		buf = append(buf, one[0])
		if len(buf) > MaxLine {
			return errors.New("рядок протоколу задовгий")
		}
	}
	return json.Unmarshal(buf, v)
}

// WriteFrame writes one data frame. An empty p writes the end marker.
func WriteFrame(w io.Writer, p []byte) error {
	if len(p) > MaxFrame {
		return fmt.Errorf("кадр %d байт більший за ліміт", len(p))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(p)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(p) == 0 {
		return nil
	}
	_, err := w.Write(p)
	return err
}

// ReadFrame reads one frame into buf and returns the payload. A zero-length
// result means the end marker.
func ReadFrame(r io.Reader, buf []byte) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrame || int(n) > len(buf) {
		return nil, fmt.Errorf("кадр %d байт більший за ліміт", n)
	}
	if _, err := io.ReadFull(r, buf[:n]); err != nil {
		return nil, err
	}
	return buf[:n], nil
}
