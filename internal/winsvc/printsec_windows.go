//go:build windows

package winsvc

// Delegated print administration for the service account.
//
// The services do not run as LocalSystem (see account_windows.go), so the
// right to create printer queues and ports is given to the service account
// alone, through the print server's security descriptor ("Manage Server" in
// Print Management), instead of through membership in Administrators.
// Microsoft: "Assign Delegated Print Administrator and Printer Permission
// Settings" — Manage Server lets a standard user create and delete queues
// (with drivers that are already installed) and add, delete and configure
// ports. OpenPrinter with a NULL name opens the local print server; level 3
// of GetPrinter/SetPrinter reads and writes a security descriptor.

import (
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"slipdrukka/internal/winprint"
)

var (
	winspool         = windows.NewLazySystemDLL("winspool.drv")
	procOpenPrinterW = winspool.NewProc("OpenPrinterW")
	procClosePrinter = winspool.NewProc("ClosePrinter")
	procGetPrinterW  = winspool.NewProc("GetPrinterW")
	procSetPrinterW  = winspool.NewProc("SetPrinterW")
)

var redirectedPort = regexp.MustCompile(`^TS\d+$`)

const (
	serverAllAccess  = 0x000F0003 // SERVER_ALL_ACCESS
	printerAllAccess = 0x000F000C // PRINTER_ALL_ACCESS
)

// printerDefaults mirrors PRINTER_DEFAULTSW.
type printerDefaults struct {
	Datatype      *uint16
	DevMode       uintptr
	DesiredAccess uint32
}

// printerInfo3 mirrors PRINTER_INFO_3.
type printerInfo3 struct{ SecurityDescriptor *windows.SECURITY_DESCRIPTOR }

// openPrintObject opens a printer, or the local print server when name is "".
func openPrintObject(name string, access uint32) (windows.Handle, error) {
	var p *uint16
	if name != "" {
		var err error
		if p, err = windows.UTF16PtrFromString(name); err != nil {
			return 0, err
		}
	}
	def := printerDefaults{DesiredAccess: access}
	var h windows.Handle
	r, _, e := procOpenPrinterW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(&def)))
	if r == 0 {
		return 0, e
	}
	return h, nil
}

func printSDDL(h windows.Handle) (string, error) {
	var needed uint32
	procGetPrinterW.Call(uintptr(h), 3, 0, 0, uintptr(unsafe.Pointer(&needed)))
	if needed == 0 {
		return "", errors.New("GetPrinter: дескриптор безпеки недоступний")
	}
	buf := make([]byte, needed)
	r, _, e := procGetPrinterW.Call(uintptr(h), 3, uintptr(unsafe.Pointer(&buf[0])), uintptr(needed), uintptr(unsafe.Pointer(&needed)))
	if r == 0 {
		return "", e
	}
	info := (*printerInfo3)(unsafe.Pointer(&buf[0]))
	if info.SecurityDescriptor == nil {
		return "", errors.New("GetPrinter: дескриптор безпеки порожній")
	}
	s := info.SecurityDescriptor.String()
	runtime.KeepAlive(buf)
	return s, nil
}

func setPrintDACL(h windows.Handle, dacl string) error {
	sd, err := windows.SecurityDescriptorFromString(dacl)
	if err != nil {
		return err
	}
	info := printerInfo3{sd}
	r, _, e := procSetPrinterW.Call(uintptr(h), 3, uintptr(unsafe.Pointer(&info)), 0)
	runtime.KeepAlive(sd)
	if r == 0 {
		return e
	}
	return nil
}

// PrintSDDL returns the security descriptor of a printer, or of the local
// print server when name is "".
func PrintSDDL(name string) (string, error) {
	access := uint32(windows.READ_CONTROL)
	h, err := openPrintObject(name, access)
	if err != nil {
		return "", err
	}
	defer procClosePrinter.Call(uintptr(h))
	return printSDDL(h)
}

func editPrintDACL(name string, access uint32, sid string, add []string) error {
	h, err := openPrintObject(name, access)
	if err != nil {
		return err
	}
	defer procClosePrinter.Call(uintptr(h))
	sddl, err := printSDDL(h)
	if err != nil {
		return err
	}
	dacl, err := daclWith(sddl, sid, add)
	if err != nil {
		return err
	}
	return setPrintDACL(h, dacl)
}

func accountSID(account string) (string, error) {
	sid, _, _, err := windows.LookupSID("", account)
	if err != nil {
		return "", fmt.Errorf("обліковий запис %s: %w", account, err)
	}
	return sid.String(), nil
}

// GrantPrintAdmin makes account a delegated print administrator of this
// computer: Manage Server on the print server, full management of printers
// created from now on (they inherit it from the print server) and of every
// printer that already exists. It does not give the right to change
// permissions or owner of the print server. A printer that could not be
// changed is reported in skipped and does not count as a failure.
func GrantPrintAdmin(account string) (skipped []string, err error) {
	sid, err := accountSID(account)
	if err != nil {
		return nil, err
	}
	if err := editPrintDACL("", serverAllAccess, sid, serverACEs(sid)); err != nil {
		return nil, fmt.Errorf("права на сервер друку: %w", err)
	}
	list, err := localPrinters()
	if err != nil {
		return nil, fmt.Errorf("список принтерів: %w", err)
	}
	for _, p := range list {
		// Printers that Remote Desktop brings along from a user's own computer
		// (ports TS001, TS002...) belong to that user's session, not to us.
		if redirectedPort.MatchString(p.Port) {
			continue
		}
		if err := editPrintDACL(p.Name, printerAllAccess, sid, printerACEs(sid)); err != nil {
			skipped = append(skipped, fmt.Sprintf("«%s»: %v", p.Name, err))
		}
	}
	return skipped, nil
}

// RevokePrintAdmin removes what GrantPrintAdmin added.
func RevokePrintAdmin(account string) error {
	sid, err := accountSID(account)
	if err != nil {
		return err
	}
	var failed []string
	if err := editPrintDACL("", serverAllAccess, sid, nil); err != nil {
		failed = append(failed, fmt.Sprintf("сервер друку: %v", err))
	}
	list, _ := localPrinters()
	for _, p := range list {
		if err := editPrintDACL(p.Name, printerAllAccess, sid, nil); err != nil {
			failed = append(failed, fmt.Sprintf("«%s»: %v", p.Name, err))
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

func localPrinters() ([]winprint.PrinterInfo, error) {
	sp, err := winprint.New()
	if err != nil {
		return nil, err
	}
	return sp.List()
}

// Access masks (winspool.h): SERVER_ACCESS_ADMINISTER 0x1, SERVER_ACCESS_ENUMERATE 0x2,
// PRINTER_ACCESS_ADMINISTER 0x4, PRINTER_ACCESS_USE 0x8, JOB_ACCESS_ADMINISTER 0x10,
// JOB_ACCESS_READ 0x20, STANDARD_RIGHTS_REQUIRED 0xF0000, READ_CONTROL 0x20000.
//
// On printers the account gets what Windows itself gives to whoever creates
// a printer (PRINTER_ALL_ACCESS): measured on Windows 11, Set-Printer and
// Rename-Printer are refused with less, because they open the printer with
// all access rights. On the print server it gets Manage Server only, without
// the right to change the server's permissions or owner.
func serverACEs(sid string) []string {
	return []string{
		"(A;;0x20003;;;" + sid + ")",     // the print server itself: Manage Server
		"(A;CIIO;0xf000c;;;" + sid + ")", // printers created later, by anyone
		"(A;OIIO;0xf0030;;;" + sid + ")", // their documents
	}
}

func printerACEs(sid string) []string {
	return []string{
		"(A;;0xf000c;;;" + sid + ")",
		"(A;OIIO;0xf0030;;;" + sid + ")",
	}
}
