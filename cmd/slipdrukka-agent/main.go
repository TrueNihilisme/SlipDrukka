// slipdrukka-agent runs on the shop PC. See README.md.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"slipdrukka/internal/agent"
	"slipdrukka/internal/common"
	"slipdrukka/internal/winprint"
	"slipdrukka/internal/winps"
	"slipdrukka/internal/winsvc"
)

var version = "dev"

const (
	svcName    = "SlipDrukkaAgent"
	svcDisplay = "SlipDrukka Agent"
	svcDesc    = "Видає принтери цього комп'ютера на сервер SlipDrukka."
	trayName   = "SlipDrukkaTray"
	// Rule of versions before 0.8.7, which could open the point's page to
	// the local network. Removed at every privileged start.
	oldPanelRule = "SlipDrukka Agent Panel"
)

func dataDir() string {
	if d := os.Getenv("SLIPDRUKKA_DIR"); d != "" {
		return d
	}
	return winsvc.DataDir("agent")
}

func open() (*agent.Agent, error) {
	dir := dataDir()
	if err := winsvc.SecureDir(dir, svcName); err != nil {
		return nil, fmt.Errorf("тека даних %s: %w", dir, err)
	}
	spool, err := winprint.New()
	if err != nil {
		return nil, err
	}
	a, err := agent.New(dir, common.NewLogRing(common.Join(dir, "agent.log"), 300), spool)
	if err != nil {
		return nil, err
	}
	a.Version = version
	a.Admin = agent.WinAdmin{}
	// The tray icon reads the page address from HKLM, which only an
	// administrator can write: it is stored while the service still has
	// those rights (first start after an install or update).
	if winsvc.Privileged() {
		a.OnPanel = winsvc.SetPanelURL
	}
	return a, nil
}

// prepare runs once with LocalSystem rights, before the service switches to
// its own account (winsvc.LowerPrivileges).
func prepare() error {
	winsvc.RemoveFirewallRule(oldPanelRule)
	// Later the built-in driver is needed for printers added from the server
	// panel and for the service printer; install it while it is certain to work.
	return winps.EnsureGenericDriver()
}

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if winsvc.IsService() {
		err := winsvc.RunService(svcName, func(ctx context.Context) error {
			a, err := open()
			if err != nil {
				return err
			}
			// msiexec ... CODE=SD1-... leaves the code in the registry.
			if code := winsvc.TakeAgentCode(); code != "" {
				if err := a.SetCode(code); err != nil {
					a.Log.Printf("код із установника не прийнято: %v", err)
				} else {
					a.Log.Printf("код підключення отримано від установника")
				}
			}
			go winsvc.LowerPrivileges(winsvc.Limits{Service: svcName, DataDir: dataDir(), Prepare: prepare, Log: a.Log.Printf})
			return a.Run(ctx)
		})
		if err != nil {
			os.Exit(1)
		}
		return
	}
	switch cmd {
	case "", "run":
		a, err := open()
		fail(err)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		fail(a.Run(ctx))
	case "install":
		a, err := open()
		fail(err)
		if len(os.Args) > 2 { // optional: code right away
			fail(a.SetCode(os.Args[2]))
		}
		exe, err := os.Executable()
		fail(err)
		fail(winsvc.Install(svcName, svcDisplay, svcDesc, exe))
		tray := filepath.Join(filepath.Dir(exe), "slipdrukka-tray.exe")
		if _, err := os.Stat(tray); err == nil {
			if err := winsvc.SetAutostart(trayName, `"`+tray+`"`); err != nil {
				fmt.Println("Не вдалося додати значок в автозапуск:", err)
			}
		}
		fmt.Println("Службу встановлено і запущено.")
		fmt.Printf("Панель (лише стан): http://%s — налаштування в панелі сервера\n", a.WebAddr())
	case "uninstall":
		cleanup()
		fail(winsvc.Uninstall(svcName))
		fmt.Println("Службу видалено. Налаштування лишились у", dataDir())
	case "cleanup":
		// Run by the installer when the program is removed.
		cleanup()
	case "account":
		fail(account(os.Args[2:]))
	case winsvc.RestartArg: // started by the service itself, see winsvc.LowerPrivileges
		_, err := winsvc.Restart(svcName)
		fail(err)
	case "version":
		fmt.Println(version)
	default:
		fmt.Println(`slipdrukka-agent ` + version + `
  install [код]  встановити службу Windows (від адміністратора)
  uninstall      видалити службу
  run            запустити в цьому вікні
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

// cleanup takes back what the service set up outside its own folder.
// Never fails: whatever is not there is skipped.
func cleanup() {
	_ = winsvc.RevokePrintAdmin(winsvc.AccountName(svcName)) // while the account still exists
	winsvc.RemoveAutostart(trayName)
	winsvc.RemoveFirewallRule(oldPanelRule)
}
