//go:build windows

package winsvc

// Running with as few rights as possible.
//
// The installer registers the service under LocalSystem, as every installer
// can. At its first start the service prepares the computer while it still
// has those rights (data folder, print rights for its own account, firewall
// rule), then switches itself to its own virtual account
// "NT SERVICE\<name>" and restarts. From then on it is an ordinary account
// that is not a member of Administrators.
//
// Microsoft docs used: CreateService/ChangeServiceConfig (a virtual account
// is named NT SERVICE\ServiceName and takes a NULL password),
// SERVICE_REQUIRED_PRIVILEGES_INFO (the service manager removes every
// privilege that is not listed from the process token).
//
// Measured on Windows 11 with a probe service under such an account: without
// the print rights Add-Printer is refused; with them (printsec_windows.go)
// ports and printers can be created, renamed, moved and removed and the
// built-in driver installed, with SeChangeNotifyPrivilege as the only
// privilege. Firewall rules, HKLM and the service's own configuration stay
// out of reach, which is why they are set up before the switch.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"

	"slipdrukka/internal/winps"
	"slipdrukka/internal/winsys"
)

// AccountName is the virtual account of a service.
func AccountName(service string) string { return `NT SERVICE\` + service }

// Privileged reports whether this process may administer the computer: it
// runs as LocalSystem or as an elevated administrator.
func Privileged() bool {
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	ok, err := windows.Token(0).IsMember(admins)
	return err == nil && ok
}

// IsLocalSystem reports whether this process runs as LocalSystem.
func IsLocalSystem() bool {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && u.User.Sid.IsWellKnown(windows.WinLocalSystemSid)
}

// CurrentAccount names the account this process runs under (for the log).
func CurrentAccount() string {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "?"
	}
	name, domain, _, err := u.User.Sid.LookupAccount("")
	if err != nil {
		return u.User.Sid.String()
	}
	return domain + `\` + name
}

const keepSystemFile = "keep-system"

// KeepSystem reports whether the administrator chose to leave the service
// under LocalSystem ("account system").
func KeepSystem(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, keepSystemFile))
	return err == nil
}

// SetKeepSystem records or clears that choice.
func SetKeepSystem(dataDir string, keep bool) error {
	p := filepath.Join(dataDir, keepSystemFile)
	if !keep {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(p, []byte("The service stays under LocalSystem. Remove with: account limited\r\n"), 0o600)
}

// SetServiceAccount switches the service between its own virtual account
// with a single privilege (limited) and LocalSystem. It takes effect at the
// next start of the service.
func SetServiceAccount(service string, limited bool) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(service)
	if err != nil {
		return err
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return err
	}
	c.Password = ""
	privileges := []string{"SeChangeNotifyPrivilege"}
	if limited {
		c.ServiceStartName = AccountName(service)
	} else {
		c.ServiceStartName = "LocalSystem"
		privileges = nil
	}
	if err := s.UpdateConfig(c); err != nil {
		return fmt.Errorf("обліковий запис служби: %w", err)
	}
	if err := setRequiredPrivileges(s.Handle, privileges); err != nil {
		return fmt.Errorf("привілеї служби: %w", err)
	}
	return nil
}

// setRequiredPrivileges writes SERVICE_REQUIRED_PRIVILEGES_INFO; an empty
// list means "no restriction".
func setRequiredPrivileges(h windows.Handle, list []string) error {
	var multi []uint16
	for _, p := range list {
		u, err := windows.UTF16FromString(p)
		if err != nil {
			return err
		}
		multi = append(multi, u...)
	}
	multi = append(multi, 0)
	if len(list) == 0 {
		multi = append(multi, 0)
	}
	info := struct{ privileges *uint16 }{&multi[0]}
	return windows.ChangeServiceConfig2(h, windows.SERVICE_CONFIG_REQUIRED_PRIVILEGES_INFO, (*byte)(unsafe.Pointer(&info)))
}

// Limits describes one service for LowerPrivileges.
type Limits struct {
	Service string
	DataDir string
	// Prepare does the service's own privileged setup (firewall rule and the
	// like). An error stops the switch.
	Prepare func() error
	Log     func(format string, args ...any)
}

// LowerPrivileges is called by a service before it opens any port
// (winsvc.Start). Under LocalSystem it prepares the computer, switches the
// service to its own account and restarts it; under any other account it
// only reports who the service runs as.
//
// restarting: the switch is done and the restart is on its way — the caller
// must not start its work in this process. problem: the switch failed and
// the service stays under LocalSystem; printing must never stop because
// rights could not be reduced, so the caller runs anyway and shows problem
// in the admin page (SystemWarning).
func LowerPrivileges(l Limits) (restarting bool, problem string) {
	if !IsLocalSystem() {
		l.Log("служба працює від імені %s", CurrentAccount())
		return false, ""
	}
	if KeepSystem(l.DataDir) {
		l.Log("служба працює з правами SYSTEM: так вибрано командою «account system»")
		return false, ""
	}
	// Let the installer see the service running before it is restarted.
	// Nothing listens on the network meanwhile.
	time.Sleep(5 * time.Second)
	account := AccountName(l.Service)
	fail := func(what string, err error) (bool, string) {
		l.Log("ПОМИЛКА: службу не вдалося перевести на обмежений обліковий запис (%s: %v) — працює далі з правами SYSTEM", what, err)
		return false, SystemWarning
	}
	if err := SecureDir(l.DataDir, l.Service); err != nil {
		return fail("тека даних", err)
	}
	warn, err := GrantPrintAdmin(account)
	if err != nil {
		return fail("права на друк", err)
	}
	for _, w := range warn {
		l.Log("права на принтер не видано: %s", w)
	}
	if l.Prepare != nil {
		if err := l.Prepare(); err != nil {
			return fail("підготовка", err)
		}
	}
	_ = EnsureRecovery(l.Service)
	if err := SetServiceAccount(l.Service, true); err != nil {
		return fail("налаштування служби", err)
	}
	l.Log("службу переведено на обліковий запис %s (без прав адміністратора) — перезапускаю", account)
	if err := spawnRestart(); err != nil {
		l.Log("перезапуск не вдався (%v) — новий обліковий запис запрацює з наступного запуску служби", err)
		return false, SystemWarning
	}
	return true, ""
}

// RestartArg is the hidden command both programs accept: it restarts their
// own service from a separate process, so the service can restart itself.
const RestartArg = "svc-restart"

func spawnRestart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, RestartArg)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
	return cmd.Start()
}

// AddProgramFirewallRule creates an inbound allow rule for one program,
// whatever port it listens on. A service without administrator rights cannot
// change firewall rules, so the rule must not depend on a port number that
// can be changed later in the admin page.
//
// An existing rule is kept (0.16.0): only its program path is refreshed. The
// administrator may have narrowed it to the points port and the shops'
// addresses (command in the admin page, Settings); an upgrade starts the
// service as LocalSystem again and used to recreate the rule wide open.
func AddProgramFirewallRule(name, exe, protocol string) error {
	netsh := winsys.Exe("netsh.exe")
	if exec.Command(netsh, "advfirewall", "firewall", "show", "rule", "name="+name).Run() == nil {
		out, err := exec.Command(netsh, "advfirewall", "firewall", "set", "rule", "name="+name, "new", "program="+exe).CombinedOutput()
		if err != nil {
			return fmt.Errorf("брандмауер: %v: %s", err, out)
		}
		return nil
	}
	desc := "SlipDrukka: inbound " + protocol + " for the SlipDrukka service. Allowed source addresses are checked by the service itself (admin page: 'Points may connect from')."
	out, err := exec.Command(netsh, "advfirewall", "firewall", "add", "rule", "name="+name, "description="+desc,
		"dir=in", "action=allow", "protocol="+protocol, "program="+exe).CombinedOutput()
	if err != nil {
		return fmt.Errorf("брандмауер: %v: %s", err, out)
	}
	return nil
}

// ReadFirewallRules reads the rules with these display names. Reading needs
// no administrator rights. Missing rules are left out.
func ReadFirewallRules(names ...string) ([]FirewallRule, error) {
	var out []FirewallRule
	for _, n := range names {
		b, err := winps.Run(`$r = Get-NetFirewallRule -DisplayName $env:SD_RULE -ErrorAction SilentlyContinue
if (-not $r) { '[]'; return }
@($r | ForEach-Object {
  $p = $_ | Get-NetFirewallPortFilter
  [pscustomobject]@{ name = $_.DisplayName; protocol = [string]$p.Protocol; ports = @($p.LocalPort | ForEach-Object { [string]$_ }); remote = @(($_ | Get-NetFirewallAddressFilter).RemoteAddress | ForEach-Object { [string]$_ }) }
}) | ConvertTo-Json -Compress -Depth 3`, map[string]string{"SD_RULE": n})
		if err != nil {
			return nil, err
		}
		var list []FirewallRule
		if err := json.Unmarshal(b, &list); err != nil {
			var one FirewallRule
			if json.Unmarshal(b, &one) != nil {
				return nil, fmt.Errorf("брандмауер: незрозуміла відповідь: %s", b)
			}
			list = []FirewallRule{one}
		}
		out = append(out, list...)
	}
	return out, nil
}

var errNotElevated = errors.New("потрібні права адміністратора")

// RequirePrivileged is for commands that only make sense elevated.
func RequirePrivileged() error {
	if !Privileged() {
		return errNotElevated
	}
	return nil
}
