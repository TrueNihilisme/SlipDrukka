//go:build windows

package winprint

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	winspool             = windows.NewLazySystemDLL("winspool.drv")
	procEnumPrintersW    = winspool.NewProc("EnumPrintersW")
	procOpenPrinterW     = winspool.NewProc("OpenPrinterW")
	procClosePrinter     = winspool.NewProc("ClosePrinter")
	procStartDocPrinterW = winspool.NewProc("StartDocPrinterW")
	procStartPagePrinter = winspool.NewProc("StartPagePrinter")
	procWritePrinter     = winspool.NewProc("WritePrinter")
	procEndPagePrinter   = winspool.NewProc("EndPagePrinter")
	procEndDocPrinter    = winspool.NewProc("EndDocPrinter")
	procAbortPrinter     = winspool.NewProc("AbortPrinter")
	procEnumDriversW     = winspool.NewProc("EnumPrinterDriversW")
)

// driverInfo2 mirrors DRIVER_INFO_2W.
type driverInfo2 struct {
	Version     uint32
	Name        *uint16
	Environment *uint16
	DriverPath  *uint16
	DataFile    *uint16
	ConfigFile  *uint16
}

func (spooler) DriverVersions() (map[string]int, error) {
	var needed, returned uint32
	procEnumDriversW.Call(0, 0, 2, 0, 0, uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if needed == 0 {
		return map[string]int{}, nil
	}
	buf := make([]byte, needed)
	r, _, err := procEnumDriversW.Call(0, 0, 2, uintptr(unsafe.Pointer(&buf[0])), uintptr(needed),
		uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if r == 0 {
		return nil, fmt.Errorf("EnumPrinterDrivers: %w", err)
	}
	out := map[string]int{}
	for _, d := range unsafe.Slice((*driverInfo2)(unsafe.Pointer(&buf[0])), returned) {
		out[windows.UTF16PtrToString(d.Name)] = int(d.Version)
	}
	return out, nil
}

const (
	printerEnumLocal       = 0x2
	printerEnumConnections = 0x4
)

// printerInfo2 mirrors PRINTER_INFO_2W.
type printerInfo2 struct {
	ServerName         *uint16
	PrinterName        *uint16
	ShareName          *uint16
	PortName           *uint16
	DriverName         *uint16
	Comment            *uint16
	Location           *uint16
	DevMode            uintptr
	SepFile            *uint16
	PrintProcessor     *uint16
	Datatype           *uint16
	Parameters         *uint16
	SecurityDescriptor uintptr
	Attributes         uint32
	Priority           uint32
	DefaultPriority    uint32
	StartTime          uint32
	UntilTime          uint32
	Status             uint32
	Jobs               uint32
	AveragePPM         uint32
}

// docInfo1 mirrors DOC_INFO_1W.
type docInfo1 struct {
	DocName    *uint16
	OutputFile *uint16
	Datatype   *uint16
}

type spooler struct{}

// New returns the real Windows spooler.
func New() (Spooler, error) { return spooler{}, nil }

func (spooler) List() ([]PrinterInfo, error) {
	flags := uintptr(printerEnumLocal | printerEnumConnections)
	var needed, returned uint32
	procEnumPrintersW.Call(flags, 0, 2, 0, 0,
		uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if needed == 0 {
		return nil, nil
	}
	buf := make([]byte, needed)
	r, _, err := procEnumPrintersW.Call(flags, 0, 2,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(needed),
		uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if r == 0 {
		return nil, fmt.Errorf("EnumPrinters: %w", err)
	}
	items := unsafe.Slice((*printerInfo2)(unsafe.Pointer(&buf[0])), returned)
	out := make([]PrinterInfo, 0, returned)
	for _, p := range items {
		out = append(out, PrinterInfo{
			Name:   windows.UTF16PtrToString(p.PrinterName),
			Driver: windows.UTF16PtrToString(p.DriverName),
			Port:   windows.UTF16PtrToString(p.PortName),
			Status: p.Status,
			Jobs:   p.Jobs,
		})
	}
	return out, nil
}

type job struct {
	h      windows.Handle
	closed bool
}

func (spooler) Open(printer, docName string) (Job, error) {
	name, err := windows.UTF16PtrFromString(printer)
	if err != nil {
		return nil, err
	}
	var h windows.Handle
	if r, _, err := procOpenPrinterW.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&h)), 0); r == 0 {
		return nil, fmt.Errorf("OpenPrinter %q: %w", printer, err)
	}
	doc := docInfo1{
		DocName:  windows.StringToUTF16Ptr(docName),
		Datatype: windows.StringToUTF16Ptr("RAW"), // pass bytes through untouched
	}
	if r, _, err := procStartDocPrinterW.Call(uintptr(h), 1, uintptr(unsafe.Pointer(&doc))); r == 0 {
		procClosePrinter.Call(uintptr(h))
		return nil, fmt.Errorf("StartDocPrinter: %w", err)
	}
	if r, _, err := procStartPagePrinter.Call(uintptr(h)); r == 0 {
		procEndDocPrinter.Call(uintptr(h))
		procClosePrinter.Call(uintptr(h))
		return nil, fmt.Errorf("StartPagePrinter: %w", err)
	}
	return &job{h: h}, nil
}

func (j *job) Write(p []byte) (int, error) {
	total := 0
	for total < len(p) {
		var written uint32
		chunk := p[total:]
		r, _, err := procWritePrinter.Call(uintptr(j.h), uintptr(unsafe.Pointer(&chunk[0])),
			uintptr(len(chunk)), uintptr(unsafe.Pointer(&written)))
		if r == 0 {
			return total, fmt.Errorf("WritePrinter: %w", err)
		}
		if written == 0 {
			return total, errors.New("WritePrinter: 0 байт записано")
		}
		total += int(written)
	}
	return total, nil
}

func (j *job) Close() error {
	if j.closed {
		return nil
	}
	j.closed = true
	procEndPagePrinter.Call(uintptr(j.h))
	r, _, err := procEndDocPrinter.Call(uintptr(j.h))
	procClosePrinter.Call(uintptr(j.h))
	if r == 0 {
		return fmt.Errorf("EndDocPrinter: %w", err)
	}
	return nil
}

func (j *job) Abort() error {
	if j.closed {
		return nil
	}
	j.closed = true
	procAbortPrinter.Call(uintptr(j.h))
	procClosePrinter.Call(uintptr(j.h))
	return nil
}
