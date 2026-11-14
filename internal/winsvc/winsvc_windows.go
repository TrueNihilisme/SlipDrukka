//go:build windows

// Package winsvc wraps the Windows bits both programs need at install time:
// the service itself and its account (account_windows.go), a locked-down
// data folder (secdir_windows.go), print rights for the service account
// (printsec_windows.go), a firewall rule and the autostart entry for the
// tray icon.
package winsvc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"slipdrukka/internal/winsys"
)

func IsService() bool {
	ok, _ := svc.IsWindowsService()
	return ok
}

type handler struct{ run func(context.Context) error }

func (h handler) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
				}
				return false, 0
			}
		case err := <-done:
			cancel()
			if err != nil {
				return false, 1
			}
			return false, 0
		}
	}
}

// RunService hands control to the service manager. Restart-on-failure is
// (re)applied at every start, so it is in place whether the service was
// registered by "install" or by the MSI (which cannot set it).
func RunService(name string, run func(context.Context) error) error {
	_ = EnsureRecovery(name)
	return svc.Run(name, handler{run})
}

func recoveryActions() []mgr.RecoveryAction {
	return []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
}

// EnsureRecovery sets "restart the service" on failure.
func EnsureRecovery(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.SetRecoveryActions(recoveryActions(), 86400)
}

// Install registers exe as an auto-start service that restarts on failure.
func Install(name, display, desc, exe string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("диспетчер служб: %w (запустіть від адміністратора)", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(name); err == nil {
		s.Close()
		return fmt.Errorf("служба %s вже встановлена", name)
	}
	s, err := m.CreateService(name, exe, mgr.Config{
		DisplayName: display, Description: desc, StartType: mgr.StartAutomatic,
	})
	if err != nil {
		return err
	}
	defer s.Close()
	_ = s.SetRecoveryActions(recoveryActions(), 86400)
	return s.Start()
}

func Uninstall(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("служби %s немає", name)
	}
	defer s.Close()
	_, _ = s.Control(svc.Stop)
	for i := 0; i < 20; i++ {
		q, err := s.Query()
		if err != nil || q.State == svc.Stopped {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	return s.Delete()
}

// DataDir is %ProgramData%\SlipDrukka\<sub>.
func DataDir(sub string) string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "SlipDrukka", sub)
}

// AddFirewallRule (re)creates an inbound allow rule for a TCP port. With
// remote addresses (IPs or CIDR) the rule only admits those sources.
func AddFirewallRule(name, protocol string, port int, remote ...string) error {
	_ = exec.Command(winsys.Exe("netsh.exe"), "advfirewall", "firewall", "delete", "rule", "name="+name).Run()
	desc := "SlipDrukka: inbound " + protocol + " " + strconv.Itoa(port) + ". Managed by the SlipDrukka service — change it in the SlipDrukka admin page, not here (it is rewritten at every service start)."
	if len(remote) > 0 {
		desc += " Allowed sources are set under 'Points may connect from'."
	}
	args := []string{"advfirewall", "firewall", "add", "rule", "name=" + name, "description=" + desc,
		"dir=in", "action=allow", "protocol=" + protocol, "localport=" + strconv.Itoa(port)}
	if len(remote) > 0 {
		args = append(args, "remoteip="+strings.Join(remote, ","))
	}
	out, err := exec.Command(winsys.Exe("netsh.exe"), args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("брандмауер: %v: %s", err, out)
	}
	return nil
}

func RemoveFirewallRule(name string) {
	_ = exec.Command(winsys.Exe("netsh.exe"), "advfirewall", "firewall", "delete", "rule", "name="+name).Run()
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// SetAutostart starts cmd at every user logon (all users).
func SetAutostart(name, cmd string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, cmd)
}

func RemoveAutostart(name string) {
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, runKey, registry.SET_VALUE); err == nil {
		k.DeleteValue(name)
		k.Close()
	}
}

const appKey = `Software\SlipDrukka`

// SetPanelURL stores where the agent page listens. It lives in the registry
// (readable by every user) because the tray icon runs as the logged-on user
// and cannot read the admin-only data folder.
func SetPanelURL(url string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, appKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("AgentPanelURL", url)
}

// PanelURL returns the stored agent page address, or "" if none.
func PanelURL() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, appKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("AgentPanelURL")
	if err != nil {
		return ""
	}
	return v
}

// setupKey is where the installer leaves the connection code
// (msiexec ... CODE=SD1-...). The installer closes this key to everyone but
// SYSTEM and Administrators (MsiLockPermissionsEx, see build.sh): the code is
// the point's password to the server and must not be readable by other
// users of the computer, even for the seconds until the service picks it up.
const setupKey = appKey + `\Setup`

// TakeAgentCode returns the connection code the installer wrote and removes
// it, so a later change is not overwritten. Installers before 0.8.9 wrote it
// next to the page address, where every user could read it; that place is
// still checked.
func TakeAgentCode() string {
	for _, path := range []string{setupKey, appKey} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.SET_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetStringValue("AgentCode")
		if err == nil {
			k.DeleteValue("AgentCode")
		}
		k.Close()
		if err == nil && v != "" {
			return v
		}
	}
	return ""
}

// Restart stops and starts the service if it is installed; it returns false
// when there is no such service.
func Restart(name string) (bool, error) {
	m, err := mgr.Connect()
	if err != nil {
		return false, err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return false, nil
	}
	defer s.Close()
	if q, err := s.Query(); err == nil && q.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil {
			return true, err
		}
		for i := 0; i < 40; i++ {
			q, err := s.Query()
			if err != nil || q.State == svc.Stopped {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	return true, s.Start()
}
