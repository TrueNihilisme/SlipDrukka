// slipdrukka-server runs next to Windows Server. See README.md.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"

	"slipdrukka/internal/common"
	"slipdrukka/internal/server"
	"slipdrukka/internal/winpeer"
	"slipdrukka/internal/winsvc"
	"slipdrukka/internal/winsys"
)

var version = "dev"

const (
	svcName      = "SlipDrukkaServer"
	svcDisplay   = "SlipDrukka Server"
	svcDesc      = "Приймає підключення точок SlipDrukka і видає їхні принтери як локальні порти."
	firewallRule = "SlipDrukka Server"
	panelRule    = "SlipDrukka Server Panel"
)

func dataDir() string {
	if d := os.Getenv("SLIPDRUKKA_DIR"); d != "" {
		return d
	}
	return winsvc.DataDir("server")
}

func open() (*server.Server, error) {
	dir := dataDir()
	if err := winsvc.SecureDir(dir, svcName); err != nil {
		return nil, fmt.Errorf("тека даних %s: %w", dir, err)
	}
	log := common.NewLogRing(common.Join(dir, "server.log"), 300)
	if !winsvc.IsService() && os.Getenv("SLIPDRUKKA_DIR") != "" {
		log = common.NewLogRing("", 300)
	}
	s, err := server.New(dir, log, server.PowerShellAdmin{})
	if err != nil {
		return nil, err
	}
	s.Version = version
	// On a computer shared by many people (a terminal server) only the print
	// spooler may feed the local printer ports, and failed sign-ins are
	// counted per Windows session. A console run is for testing: open.
	if winsvc.IsService() {
		s.LocalClient = winpeer.CheckPrintClient
		winpeer.Warn = func(msg string) { s.Log.Printf("УВАГА: %s", msg) }
	}
	s.LoginKey = winpeer.LoginKey
	// Firewall rules can only be rewritten with administrator rights. The
	// service normally gives those up (winsvc.LowerPrivileges): then one rule
	// for the program is created beforehand (prepare) and the list of allowed
	// addresses is enforced by the server itself. Only a service the
	// administrator left under LocalSystem, or a console run, keeps rewriting
	// the port rule with the allowed addresses.
	s.FirewallRead = func(names ...string) ([]server.FWRule, error) {
		rules, err := winsvc.ReadFirewallRules(names...)
		out := make([]server.FWRule, len(rules))
		for i, r := range rules {
			out[i] = server.FWRule(r)
		}
		return out, err
	}
	if winsvc.Privileged() && (!winsvc.IsService() || winsvc.KeepSystem(dir)) {
		s.FirewallAdd = func(name, protocol string, port int, remote []string) error {
			return winsvc.AddFirewallRule(name, protocol, port, remote...)
		}
		s.FirewallRemove = winsvc.RemoveFirewallRule
	}
	return s, nil
}

// prepare runs once with LocalSystem rights, before the service switches to
// its own account: one firewall rule for this program, whatever ports are
// chosen later in the admin page.
func prepare() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	winsvc.RemoveFirewallRule(panelRule)
	if err := winsvc.AddProgramFirewallRule(firewallRule, exe, "TCP"); err != nil {
		return err
	}
	// The optional WireGuard channel (UDP, same port number). Harmless when
	// WireGuard is off: nothing listens on UDP then.
	return winsvc.AddProgramFirewallRule(firewallRule+" WireGuard", exe, "UDP")
}

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if winsvc.IsService() {
		err := winsvc.RunService(svcName, func(ctx context.Context) error {
			s, err := open()
			if err != nil {
				return err
			}
			go winsvc.LowerPrivileges(winsvc.Limits{Service: svcName, DataDir: dataDir(), Prepare: prepare, Log: s.Log.Printf})
			return s.Run(ctx)
		})
		if err != nil {
			os.Exit(1)
		}
		return
	}
	switch cmd {
	case "", "run":
		s, err := open()
		fail(err)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		fail(s.Run(ctx))
	case "install":
		s, err := open()
		fail(err)
		_, portStr, err := net.SplitHostPort(s.ListenAddr())
		fail(err)
		port, _ := strconv.Atoi(portStr)
		exe, err := os.Executable()
		fail(err)
		fail(winsvc.AddProgramFirewallRule(firewallRule, exe, "TCP"))
		fail(winsvc.AddProgramFirewallRule(firewallRule+" WireGuard", exe, "UDP"))
		fail(winsvc.Install(svcName, svcDisplay, svcDesc, exe))
		fmt.Println("Службу встановлено і запущено.")
		fmt.Printf("Панель керування: http://%s — вхід обліковим записом адміністратора Windows\n", s.WebAddr())
		fmt.Printf("Точки підключаються на порт %d: TCP і UDP (WireGuard) — прокиньте обидва на роутері сервера.\n", port)
	case "uninstall":
		cleanup()
		fail(winsvc.Uninstall(svcName))
		fmt.Println("Службу видалено. Налаштування лишились у", dataDir())
	case "panel":
		// The "SlipDrukka panel" shortcut: open the admin panel in the browser.
		openPanel()
	case "cleanup":
		// Run by the installer when the program is removed (the installer
		// removes the service itself): leave no firewall rules or rights behind.
		cleanup()
	case "account":
		fail(account(os.Args[2:]))
	case winsvc.RestartArg: // started by the service itself, see winsvc.LowerPrivileges
		_, err := winsvc.Restart(svcName)
		fail(err)
	case "set-password":
		s, err := open()
		fail(err)
		// Emergency panel password, for when Windows sign-in is impossible
		// (e.g. a policy forbids it). "set-password -" removes it.
		pw := common.RandomPassword()
		if len(os.Args) > 2 {
			pw = os.Args[2]
		}
		if pw == "-" {
			pw = ""
		}
		fail(s.SetPassword(pw))
		// The service keeps the password in memory: restart it so the new
		// one takes effect right away (and old sessions end).
		if found, err := winsvc.Restart(svcName); err != nil {
			fmt.Println("Новий пароль збережено, але службу не вдалося перезапустити:", err)
			fmt.Println("Перезапустіть службу вручну — до того діє старий пароль.")
		} else if pw == "" {
			fmt.Println("Аварійний пароль панелі прибрано")
		} else if found {
			fmt.Println("Аварійний пароль панелі:", pw, "(службу перезапущено; вхід: залиште ім'я порожнім)")
		} else {
			fmt.Println("Аварійний пароль панелі:", pw, "(вхід: залиште ім'я порожнім)")
		}
	case "version":
		fmt.Println(version)
	default:
		fmt.Println(`slipdrukka-server ` + version + `
  install        встановити службу Windows (від адміністратора)
  uninstall      видалити службу
  run            запустити в цьому вікні
  set-password   [пароль|-] — аварійний пароль панелі (звичайний вхід — адмін Windows)
  account        limited | system — від чийого імені працює служба
  version`)
	}
}

// account switches the service between its own limited account (the
// default) and LocalSystem, for the rare computer where the limited account
// turns out not to be enough.
func account(args []string) error {
	if len(args) != 1 || (args[0] != "limited" && args[0] != "system") {
		return fmt.Errorf("вкажіть limited (власний обліковий запис служби, без прав адміністратора) або system")
	}
	if err := winsvc.RequirePrivileged(); err != nil {
		return err
	}
	dir := dataDir()
	if err := winsvc.SecureDir(dir, svcName); err != nil {
		return err
	}
	if err := winsvc.SetKeepSystem(dir, args[0] == "system"); err != nil {
		return err
	}
	// Always back to LocalSystem first: with "limited" the service prepares
	// the computer and lowers its own rights at the next start.
	if err := winsvc.SetServiceAccount(svcName, false); err != nil {
		return err
	}
	if _, err := winsvc.Restart(svcName); err != nil {
		return err
	}
	if args[0] == "system" {
		fmt.Println("Служба працює з правами SYSTEM.")
	} else {
		fmt.Println("Служба за кілька секунд перейде на власний обліковий запис", winsvc.AccountName(svcName))
	}
	return nil
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "Помилка:", err)
		os.Exit(1)
	}
}

// cleanup takes back what the service set up outside its own folder: its
// rights on the print server and its firewall rules. Never fails: whatever
// is not there is skipped.
func cleanup() {
	_ = winsvc.RevokePrintAdmin(winsvc.AccountName(svcName)) // while the account still exists
	for _, rule := range []string{firewallRule, firewallRule + " WireGuard", panelRule} {
		winsvc.RemoveFirewallRule(rule)
	}
}

// openPanel opens the admin panel at the address from the settings (the
// panel may have been moved to another port or opened to the network).
func openPanel() {
	var cfg struct {
		Web string `json:"web"`
	}
	_ = common.LoadJSON(common.Join(dataDir(), "server.json"), &cfg)
	port := "7330"
	if _, p, err := net.SplitHostPort(cfg.Web); err == nil && p != "" {
		port = p
	}
	exec.Command(winsys.Exe("rundll32.exe"), "url.dll,FileProtocolHandler", "http://127.0.0.1:"+port+"/").Start()
}
