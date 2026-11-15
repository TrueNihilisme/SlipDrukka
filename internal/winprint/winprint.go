// Package winprint talks to the Windows print spooler: it lists printers and
// sends RAW jobs, so bytes rendered by the driver on the server reach the
// printer unchanged.
package winprint

import (
	"errors"
	"io"
)

// PrinterInfo describes one printer installed in Windows.
type PrinterInfo struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
	Port   string `json:"port"`
	Status uint32 `json:"status"`
	Jobs   uint32 `json:"jobs"`
}

// Paper is a paper size in a printer's settings: Kind is the Windows paper
// number (256 = a custom size), Width and Length are tenths of a millimetre.
type Paper struct {
	Kind   int `json:"kind"`
	Width  int `json:"width"`
	Length int `json:"length"`
}

// ErrPaperRejected: the printer's driver does not take this paper size.
var ErrPaperRejected = errors.New("winprint: the driver rejected the paper size")

// Spooler is what the agent needs from the OS. Tests use a fake.
type Spooler interface {
	List() ([]PrinterInfo, error)
	// Open starts a RAW job. Close finishes it, Abort cancels it.
	Open(printer, docName string) (Job, error)
	// PrintXPS prints an XPS or OpenXPS file through the printer's own
	// driver (document mode).
	PrintXPS(printer, docName, path string) error
	// DriverVersions maps installed driver names to their model version
	// (3 = classic GDI driver, 4 = v4 XPS-based driver).
	DriverVersions() (map[string]int, error)
}

type Job interface {
	io.Writer
	Close() error
	Abort() error
}
