//go:build !windows

package winsvc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

var errNotWindows = errors.New("лише для Windows")

func IsService() bool                                      { return false }
func RunService(string, func(context.Context) error) error { return errNotWindows }
func Install(string, string, string, string) error         { return errNotWindows }
func Uninstall(string) error                               { return errNotWindows }
func SecureDir(dir, _ string) error                        { return os.MkdirAll(dir, 0o700) }
func AddFirewallRule(string, string, int, ...string) error { return nil }
func AddProgramFirewallRule(string, string, string) error  { return nil }
func RemoveFirewallRule(string)                            {}
func SetAutostart(string, string) error                    { return errNotWindows }
func RemoveAutostart(string)                               {}

// DataDir off Windows is only for local testing.
func DataDir(sub string) string {
	return filepath.Join(os.TempDir(), "slipdrukka", sub)
}

func SetPanelURL(string) error { return nil }
func PanelURL() string         { return "" }

func EnsureRecovery(string) error { return nil }
func TakeAgentCode() string       { return "" }

func Restart(string) (bool, error) { return false, nil }

// Service accounts exist only on Windows.

const RestartArg = "svc-restart"

type Limits struct {
	Service string
	DataDir string
	Prepare func() error
	Log     func(format string, args ...any)
}

func AccountName(service string) string        { return `NT SERVICE\` + service }
func Privileged() bool                         { return false }
func IsLocalSystem() bool                      { return false }
func CurrentAccount() string                   { return "" }
func KeepSystem(string) bool                   { return false }
func SetKeepSystem(string, bool) error         { return errNotWindows }
func SetServiceAccount(string, bool) error     { return errNotWindows }
func LowerPrivileges(Limits)                   {}
func RequirePrivileged() error                 { return errNotWindows }
func GrantPrintAdmin(string) ([]string, error) { return nil, errNotWindows }
func RevokePrintAdmin(string) error            { return errNotWindows }
func PrintSDDL(string) (string, error)         { return "", errNotWindows }
