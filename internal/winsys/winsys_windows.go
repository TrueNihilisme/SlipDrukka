//go:build windows

// Package winsys locates Windows' own programs by full path. The services
// start powershell.exe and netsh.exe; looking them up through PATH would let
// a folder that ordinary users can write to, if one is ever on the system
// PATH, slip in its own program with the service's rights.
package winsys

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Exe returns the full path of a program that ships with Windows.
func Exe(name string) string {
	dir, err := windows.GetSystemDirectory()
	if err != nil || dir == "" {
		dir = `C:\Windows\System32`
	}
	if strings.EqualFold(name, "powershell.exe") {
		return filepath.Join(dir, "WindowsPowerShell", "v1.0", name)
	}
	return filepath.Join(dir, name)
}
