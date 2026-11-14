//go:build windows

package winprint

// Document mode on Windows.
//
// "Microsoft XPS Document Writer v4" on the server produces OpenXPS. The .NET
// print path (System.Printing.PrintQueue.AddJob) only reads Microsoft XPS:
// measured on Windows 11, XpsDocument.GetFixedDocumentSequence() returns null
// for such a file and AddJob fails. So the file is first rewritten to
// Microsoft XPS with the Windows XPS Document API (IXpsOMPackage1::WriteToFile1,
// Windows 8 / Server 2012+), then printed with AddJob(fastCopy=false), which
// renders it through the printer's own driver (XPS->GDI for GDI drivers).
//
// COM vtable indices were counted from the Windows SDK headers
// (mingw-w64 xpsobjectmodel_1.h):
//   IXpsOMObjectFactory1: CreatePackageFromFile1 = 48
//   IXpsOMPackage1:       GetDocumentType = 13, WriteToFile1 = 14

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"slipdrukka/internal/winsys"
)

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")

	clsidXpsOMObjectFactory = windows.GUID{Data1: 0xe974d26d, Data2: 0x3d9b, Data3: 0x4d47,
		Data4: [8]byte{0x88, 0xcc, 0x38, 0x72, 0xf2, 0xdc, 0x35, 0x85}}
	iidXpsOMObjectFactory1 = windows.GUID{Data1: 0x0a91b617, Data2: 0xd612, Data3: 0x4181,
		Data4: [8]byte{0xbf, 0x7c, 0xbe, 0x58, 0x24, 0xe9, 0xcc, 0x8f}}
)

const (
	clsctxInprocServer   = 0x1
	coinitMultithreaded  = 0x0
	fileAttributeNormal  = 0x80
	xpsDocTypeXPS        = 2
	xpsDocTypeOpenXPS    = 3
	vtCreatePackageFile1 = 48
	vtGetDocumentType    = 13
	vtWriteToFile1       = 14
	vtRelease            = 2
)

func hr(r uintptr) error {
	if int32(r) < 0 {
		return fmt.Errorf("HRESULT 0x%08X", uint32(r))
	}
	return nil
}

// vtable returns the method table of a COM object.
func vtable(obj unsafe.Pointer) *[64]uintptr { return *(**[64]uintptr)(obj) }

func release(obj unsafe.Pointer) {
	if obj != nil {
		syscall.SyscallN(vtable(obj)[vtRelease], uintptr(obj))
	}
}

// toMSXPS rewrites an OpenXPS file as Microsoft XPS into out. It returns
// false (and leaves out untouched) when the input already is Microsoft XPS.
func toMSXPS(in, out string) (bool, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded)
	if err := hr(r); err != nil {
		return false, fmt.Errorf("CoInitializeEx: %w", err)
	}
	defer procCoUninitialize.Call()

	var factory unsafe.Pointer
	r, _, _ = procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidXpsOMObjectFactory)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidXpsOMObjectFactory1)), uintptr(unsafe.Pointer(&factory)))
	if err := hr(r); err != nil {
		return false, fmt.Errorf("XPS OM недоступний: %w", err)
	}
	defer release(factory)

	inW, err := windows.UTF16PtrFromString(in)
	if err != nil {
		return false, err
	}
	var pkg unsafe.Pointer
	r, _, _ = syscall.SyscallN(vtable(factory)[vtCreatePackageFile1], uintptr(factory),
		uintptr(unsafe.Pointer(inW)), 0, uintptr(unsafe.Pointer(&pkg)))
	if err := hr(r); err != nil {
		return false, fmt.Errorf("документ не читається як XPS: %w", err)
	}
	defer release(pkg)

	var docType int32
	r, _, _ = syscall.SyscallN(vtable(pkg)[vtGetDocumentType], uintptr(pkg), uintptr(unsafe.Pointer(&docType)))
	if err := hr(r); err != nil {
		return false, fmt.Errorf("тип документа: %w", err)
	}
	if docType != xpsDocTypeOpenXPS {
		return false, nil
	}
	outW, err := windows.UTF16PtrFromString(out)
	if err != nil {
		return false, err
	}
	r, _, _ = syscall.SyscallN(vtable(pkg)[vtWriteToFile1], uintptr(pkg), uintptr(unsafe.Pointer(outW)),
		0, fileAttributeNormal, 0, xpsDocTypeXPS)
	if err := hr(r); err != nil {
		return false, fmt.Errorf("перетворення OpenXPS → XPS: %w", err)
	}
	return true, nil
}

// PrintXPS prints the document through the printer's own driver.
func (spooler) PrintXPS(printer, docName, path string) error {
	file := path
	conv := strings.TrimSuffix(path, ".xps") + "-ms.xps"
	converted, err := toMSXPS(path, conv)
	if err != nil {
		return err
	}
	if converted {
		file = conv
		defer os.Remove(conv)
	}
	// PrintQueue.AddJob(fastCopy=false) needs an STA thread; Windows
	// PowerShell 5.1 runs scripts in STA. Values go in through the
	// environment, never into the script text.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, winsys.Exe("powershell.exe"), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command",
		`$ErrorActionPreference='Stop'; [Console]::OutputEncoding=[Text.Encoding]::UTF8
Add-Type -AssemblyName System.Printing, ReachFramework
try {
  $q = (New-Object System.Printing.LocalPrintServer).GetPrintQueue($env:PR_PRINTER)
  $null = $q.AddJob($env:PR_DOC, $env:PR_FILE, $false)
} catch { $e = $_.Exception; while ($e.InnerException) { $e = $e.InnerException }; [Console]::Error.WriteLine($e.Message); exit 1 }`)
	cmd.Env = append(os.Environ(), "PR_PRINTER="+printer, "PR_DOC="+docName, "PR_FILE="+file)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New("друк документа: " + msg)
	}
	return nil
}
