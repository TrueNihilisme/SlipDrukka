//go:build windows

package winprint

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procDocumentPropertiesW = winspool.NewProc("DocumentPropertiesW")
	procGetPrinterW         = winspool.NewProc("GetPrinterW")
	procSetPrinterW         = winspool.NewProc("SetPrinterW")
)

// Offsets inside DEVMODEW (wingdi.h) of the fields a paper size lives in.
const (
	dmOffFields      = 72 // dmFields, uint32
	dmOffPaperSize   = 78 // dmPaperSize, int16
	dmOffPaperLength = 80 // dmPaperLength, int16, tenths of a millimetre
	dmOffPaperWidth  = 82 // dmPaperWidth, int16, tenths of a millimetre
	dmMinSize        = 84

	dmFieldPaperSize   = 0x2
	dmFieldPaperLength = 0x4
	dmFieldPaperWidth  = 0x8

	dmPaperUser = 256 // DMPAPER_USER: the size is in dmPaperWidth / dmPaperLength

	dmOutBuffer = 2
	dmInBuffer  = 8

	printerAllAccess = 0x000F000C
	printerAccessUse = 0x8
)

type printerOpenDefaults struct {
	Datatype      *uint16
	DevMode       uintptr
	DesiredAccess uint32
}

func openPrinterAccess(name string, access uint32) (windows.Handle, *uint16, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, nil, err
	}
	def := printerOpenDefaults{DesiredAccess: access}
	var h windows.Handle
	r, _, e := procOpenPrinterW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(&def)))
	if r == 0 {
		return 0, nil, fmt.Errorf("OpenPrinter: %w", e)
	}
	return h, p, nil
}

func paperOf(dm []byte) Paper {
	if len(dm) < dmMinSize {
		return Paper{}
	}
	i16 := func(off int) int { return int(int16(uint16(dm[off]) | uint16(dm[off+1])<<8)) }
	return Paper{Kind: i16(dmOffPaperSize), Width: i16(dmOffPaperWidth), Length: i16(dmOffPaperLength)}
}

// GlobalPaper reads the paper size in the printer's default settings for
// all users (PRINTER_INFO_8).
func GlobalPaper(printer string) (Paper, error) {
	h, _, err := openPrinterAccess(printer, printerAccessUse)
	if err != nil {
		return Paper{}, err
	}
	defer procClosePrinter.Call(uintptr(h))
	var needed uint32
	procGetPrinterW.Call(uintptr(h), 8, 0, 0, uintptr(unsafe.Pointer(&needed)))
	if needed == 0 {
		return Paper{}, errors.New("GetPrinter: no default settings")
	}
	buf := make([]byte, needed)
	r, _, e := procGetPrinterW.Call(uintptr(h), 8, uintptr(unsafe.Pointer(&buf[0])), uintptr(needed), uintptr(unsafe.Pointer(&needed)))
	if r == 0 {
		return Paper{}, fmt.Errorf("GetPrinter: %w", e)
	}
	dm := *(*uintptr)(unsafe.Pointer(&buf[0])) // PRINTER_INFO_8.pDevMode, points into buf
	base := uintptr(unsafe.Pointer(&buf[0]))
	if dm < base || dm+dmMinSize > base+uintptr(len(buf)) {
		return Paper{}, errors.New("GetPrinter: no default settings")
	}
	return paperOf(buf[dm-base:]), nil
}

// SetGlobalPaper writes a custom paper size (tenths of a millimetre) into
// the printer's default settings for all users. The driver checks the
// settings first and may change them; what it accepted is returned. Users
// who saved settings of their own for this printer keep those.
func SetGlobalPaper(printer string, width, length int) (Paper, error) {
	if width <= 0 || length <= 0 || width > 32767 || length > 32767 {
		return Paper{}, errors.New("paper size out of range")
	}
	h, name, err := openPrinterAccess(printer, printerAllAccess)
	if err != nil {
		return Paper{}, err
	}
	defer procClosePrinter.Call(uintptr(h))
	n, _, _ := procDocumentPropertiesW.Call(0, uintptr(h), uintptr(unsafe.Pointer(name)), 0, 0, 0)
	if int32(n) < dmMinSize {
		return Paper{}, errors.New("DocumentProperties: the driver gave no settings")
	}
	cur := make([]byte, n)
	if r, _, _ := procDocumentPropertiesW.Call(0, uintptr(h), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&cur[0])), 0, dmOutBuffer); int32(r) < 0 {
		return Paper{}, errors.New("DocumentProperties: the driver gave no settings")
	}
	put16 := func(off, v int) { cur[off], cur[off+1] = byte(v), byte(v>>8) }
	put16(dmOffPaperSize, dmPaperUser)
	put16(dmOffPaperLength, length)
	put16(dmOffPaperWidth, width)
	fields := uint32(cur[dmOffFields]) | uint32(cur[dmOffFields+1])<<8 | uint32(cur[dmOffFields+2])<<16 | uint32(cur[dmOffFields+3])<<24
	fields |= dmFieldPaperSize | dmFieldPaperLength | dmFieldPaperWidth
	cur[dmOffFields], cur[dmOffFields+1], cur[dmOffFields+2], cur[dmOffFields+3] = byte(fields), byte(fields>>8), byte(fields>>16), byte(fields>>24)
	out := make([]byte, n)
	if r, _, _ := procDocumentPropertiesW.Call(0, uintptr(h), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&out[0])), uintptr(unsafe.Pointer(&cur[0])), dmInBuffer|dmOutBuffer); int32(r) < 0 {
		return Paper{}, ErrPaperRejected
	}
	info := struct{ DevMode uintptr }{uintptr(unsafe.Pointer(&out[0]))} // PRINTER_INFO_8
	r, _, e := procSetPrinterW.Call(uintptr(h), 8, uintptr(unsafe.Pointer(&info)), 0)
	if r == 0 {
		return Paper{}, fmt.Errorf("SetPrinter: %w", e)
	}
	return paperOf(out), nil
}
