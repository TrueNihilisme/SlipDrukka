// Package winps runs short PowerShell scripts for printer administration.
// Values are passed through environment variables and never pasted into the
// script text, so names typed into the admin pages cannot inject code.
package winps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"slipdrukka/internal/winsys"
)

// Run executes script with the given environment variables added.
func Run(script string, env map[string]string) ([]byte, error) {
	if runtime.GOOS != "windows" {
		return nil, errors.New("керування принтерами доступне лише у Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, winsys.Exe("powershell.exe"), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", "$ErrorActionPreference='Stop'; [Console]::OutputEncoding=[Text.Encoding]::UTF8; "+script)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return out, nil
}

// Drivers lists printer drivers installed in Windows.
func Drivers() ([]string, error) {
	out, err := Run(`@(Get-PrinterDriver | Select-Object -ExpandProperty Name | Sort-Object -Unique) | ConvertTo-Json -Compress`, nil)
	if err != nil {
		return nil, err
	}
	return parseStrings(out)
}

// GenericDriver is the inbox driver that passes data through untouched.
const GenericDriver = "Generic / Text Only"

// EnsureGenericDriver installs the inbox Generic / Text Only driver if it is
// not installed yet.
func EnsureGenericDriver() error {
	_, err := Run(`if (-not (Get-PrinterDriver -Name $env:PR_GENERIC -ErrorAction SilentlyContinue)) { Add-PrinterDriver -Name $env:PR_GENERIC }`,
		map[string]string{"PR_GENERIC": GenericDriver})
	return err
}

// AddNetworkPrinter creates a Standard TCP/IP (RAW) port host:port and a
// Windows printer on it. The inbox Generic / Text Only driver is installed
// on demand; any other driver must already be installed.
func AddNetworkPrinter(name, host string, port int, driver string) error {
	_, err := Run(`
if (-not (Get-PrinterDriver -Name $env:PR_DRIVER -ErrorAction SilentlyContinue)) {
  if ($env:PR_DRIVER -eq $env:PR_GENERIC) { Add-PrinterDriver -Name $env:PR_DRIVER }
  else { throw ("Драйвер «" + $env:PR_DRIVER + "» не встановлено на цьому комп'ютері") }
}
if (Get-Printer -Name $env:PR_NAME -ErrorAction SilentlyContinue) { throw "Принтер з такою назвою вже є" }
if (-not (Get-PrinterPort -Name $env:PR_PORTNAME -ErrorAction SilentlyContinue)) {
  Add-PrinterPort -Name $env:PR_PORTNAME -PrinterHostAddress $env:PR_HOST -PortNumber ([uint32]$env:PR_PORT)
}
Add-Printer -Name $env:PR_NAME -DriverName $env:PR_DRIVER -PortName $env:PR_PORTNAME
`, map[string]string{"PR_NAME": name, "PR_HOST": host, "PR_PORT": itoa(port), "PR_DRIVER": driver,
		"PR_GENERIC": GenericDriver, "PR_PORTNAME": "IP_" + host + "_" + itoa(port)})
	return err
}

func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{digits[n%10]}, b...)
	}
	return string(b)
}

// PortDetail describes a Windows printer port.
type PortDetail struct {
	Name        string `json:"n"`
	Host        string `json:"h"`
	Number      int    `json:"p"`
	Description string `json:"d"`
}

// Ports lists printer ports and the USB printers Windows currently sees
// (model names as reported by the device).
func Ports() ([]PortDetail, []string, error) {
	out, err := Run(`
$ports = @(Get-PrinterPort | ForEach-Object { @{ n = $_.Name; h = [string]$_.PrinterHostAddress; p = [int]$_.PortNumber; d = [string]$_.Description } })
$usb = @(Get-PnpDevice -PresentOnly -ErrorAction SilentlyContinue | Where-Object { $_.InstanceId -like 'USBPRINT\*' } | ForEach-Object { [string]$_.FriendlyName })
@{ ports = $ports; usb = $usb } | ConvertTo-Json -Depth 4 -Compress`, nil)
	if err != nil {
		return nil, nil, err
	}
	var res struct {
		Ports json.RawMessage `json:"ports"`
		USB   json.RawMessage `json:"usb"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, nil, err
	}
	var ports []PortDetail
	if err := json.Unmarshal(res.Ports, &ports); err != nil {
		var one PortDetail
		if json.Unmarshal(res.Ports, &one) == nil {
			ports = []PortDetail{one}
		}
	}
	usb, _ := parseStrings(res.USB)
	return ports, usb, nil
}

// AddPrinterOnPort creates a printer on an existing port (e.g. USB002).
func AddPrinterOnPort(name, port, driver string) error {
	_, err := Run(`
if (-not (Get-PrinterPort -Name $env:PR_PORT -ErrorAction SilentlyContinue)) { throw ("Порту «" + $env:PR_PORT + "» немає") }
if (-not (Get-PrinterDriver -Name $env:PR_DRIVER -ErrorAction SilentlyContinue)) {
  if ($env:PR_DRIVER -eq $env:PR_GENERIC) { Add-PrinterDriver -Name $env:PR_DRIVER }
  else { throw ("Драйвер «" + $env:PR_DRIVER + "» не встановлено на цьому комп'ютері") }
}
if (Get-Printer -Name $env:PR_NAME -ErrorAction SilentlyContinue) { throw "Принтер з такою назвою вже є" }
Add-Printer -Name $env:PR_NAME -DriverName $env:PR_DRIVER -PortName $env:PR_PORT
`, map[string]string{"PR_NAME": name, "PR_PORT": port, "PR_DRIVER": driver, "PR_GENERIC": GenericDriver})
	return err
}

// SetPrinterDriver switches a Windows printer to another installed driver
// (the built-in "Generic / Text Only" is installed on demand).
func SetPrinterDriver(name, driver string) error {
	_, err := Run(`
if (-not (Get-PrinterDriver -Name $env:PR_DRIVER -ErrorAction SilentlyContinue)) {
  if ($env:PR_DRIVER -eq $env:PR_GENERIC) { Add-PrinterDriver -Name $env:PR_DRIVER }
  else { throw ("Драйвер «" + $env:PR_DRIVER + "» не встановлено на цьому комп'ютері") }
}
Set-Printer -Name $env:PR_NAME -DriverName $env:PR_DRIVER`, map[string]string{"PR_NAME": name, "PR_DRIVER": driver, "PR_GENERIC": GenericDriver})
	return err
}

// SetPrinterIP moves a printer to the Standard TCP/IP (RAW) port
// IP_<host>_<port>, creating that port if needed (same naming as
// AddNetworkPrinter). The old port is left in place.
func SetPrinterIP(name, host string, port int) error {
	_, err := Run(`
if (-not (Get-Printer -Name $env:PR_NAME -ErrorAction SilentlyContinue)) { throw "Принтера з такою назвою немає" }
if (-not (Get-PrinterPort -Name $env:PR_PORTNAME -ErrorAction SilentlyContinue)) {
  Add-PrinterPort -Name $env:PR_PORTNAME -PrinterHostAddress $env:PR_HOST -PortNumber ([uint32]$env:PR_PORT)
}
Set-Printer -Name $env:PR_NAME -PortName $env:PR_PORTNAME`, map[string]string{"PR_NAME": name, "PR_HOST": host, "PR_PORT": itoa(port),
		"PR_PORTNAME": "IP_" + host + "_" + itoa(port)})
	return err
}

// RenamePrinter renames a Windows printer.
func RenamePrinter(old, new string) error {
	_, err := Run(`
if (Get-Printer -Name $env:PR_NEW -ErrorAction SilentlyContinue) { throw "Принтер з такою назвою вже є" }
Rename-Printer -Name $env:PR_OLD -NewName $env:PR_NEW`, map[string]string{"PR_OLD": old, "PR_NEW": new})
	return err
}

// RemovePrinter deletes a Windows printer (the port stays).
func RemovePrinter(name string) error {
	_, err := Run(`Remove-Printer -Name $env:PR_NAME`, map[string]string{"PR_NAME": name})
	return err
}
