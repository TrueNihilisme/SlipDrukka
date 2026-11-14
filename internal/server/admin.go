package server

import (
	"encoding/json"
	"runtime"

	"slipdrukka/internal/winps"
)

// Admin manages printers on the Windows Server side.
type Admin interface {
	Drivers() ([]string, error)
	QueuesByPort() (map[string][]Queue, error) // port name -> queues on it
	CreateQueue(queue, driver, portName string, port int) error
	RenameQueue(old, new string) error
}

// PowerShellAdmin uses the PrintManagement module (see winps).
type PowerShellAdmin struct{}

var ps = winps.Run

func (PowerShellAdmin) Drivers() ([]string, error) { return winps.Drivers() }

// Queue is a Windows printer on this server that uses one of our ports.
type Queue struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
}

func (PowerShellAdmin) QueuesByPort() (map[string][]Queue, error) {
	if runtime.GOOS != "windows" {
		return map[string][]Queue{}, nil
	}
	out, err := ps(`@(Get-Printer | Where-Object { $_.PortName -like 'SLIPDRUKKA_*' } | ForEach-Object { @{ q = $_.Name; p = $_.PortName; d = $_.DriverName } }) | ConvertTo-Json -Compress`, nil)
	if err != nil {
		return nil, err
	}
	res := map[string][]Queue{}
	var items []struct{ Q, P, D string }
	if err := json.Unmarshal(out, &items); err != nil {
		var one struct{ Q, P, D string }
		if json.Unmarshal(out, &one) == nil && one.P != "" {
			items = append(items, one)
		}
	}
	for _, it := range items {
		res[it.P] = append(res[it.P], Queue{Name: it.Q, Driver: it.D})
	}
	return res, nil
}

func (PowerShellAdmin) RenameQueue(old, new string) error { return winps.RenamePrinter(old, new) }

func (PowerShellAdmin) CreateQueue(queue, driver, portName string, port int) error {
	_, err := ps(`
if (-not (Get-PrinterPort -Name $env:PR_PORTNAME -ErrorAction SilentlyContinue)) {
  Add-PrinterPort -Name $env:PR_PORTNAME -PrinterHostAddress '127.0.0.1' -PortNumber ([uint32]$env:PR_PORT)
}
if (Get-Printer -Name $env:PR_QUEUE -ErrorAction SilentlyContinue) { throw "Принтер з такою назвою вже є" }
Add-Printer -Name $env:PR_QUEUE -DriverName $env:PR_DRIVER -PortName $env:PR_PORTNAME
`, map[string]string{"PR_PORTNAME": portName, "PR_PORT": itoa(port), "PR_QUEUE": queue, "PR_DRIVER": driver})
	return err
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
