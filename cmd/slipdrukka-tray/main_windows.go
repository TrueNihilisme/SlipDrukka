//go:build windows

// slipdrukka-tray is the icon next to the clock. It only shows the agent's
// state and opens its page; the real work is done by the agent service.
package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"slipdrukka/internal/i18n"
	"slipdrukka/internal/winsvc"
	"slipdrukka/internal/winsys"
)

const defaultPanelURL = "http://127.0.0.1:7320/"

// panelURL follows the agent page if the admin moved it to another port.
func panelURL() string {
	if u := winsvc.PanelURL(); u != "" {
		return u
	}
	return defaultPanelURL
}

func main() {
	// "open": started from the shortcut or at the end of the installer — show
	// the shop page, and the icon if it is not there yet.
	if len(os.Args) > 1 && os.Args[1] == "open" {
		openPage()
	}
	// One icon per user session.
	name, _ := windows.UTF16PtrFromString(`Local\SlipDrukkaTray`)
	if _, err := windows.CreateMutex(nil, false, name); err == windows.ERROR_ALREADY_EXISTS {
		return
	}
	systray.Run(onReady, func() {})
}

func openPage() {
	exec.Command(winsys.Exe("rundll32.exe"), "url.dll,FileProtocolHandler", panelURL()).Start()
}

// The tray speaks the language of Windows.
var lang = i18n.System()

func tr(s string) string { return i18n.Tr(lang, s) }

// Status icons: the printer of the SlipDrukka icon, coloured by state
// (assets/tray-*.svg; green — connected, red — no connection, grey — the
// service does not answer).
var (
	//go:embed tray-ok.ico
	iconOK []byte
	//go:embed tray-bad.ico
	iconBad []byte
	//go:embed tray-idle.ico
	iconIdle []byte
)

func onReady() {
	systray.SetIcon(iconIdle)
	systray.SetTooltip("SlipDrukka")
	status := systray.AddMenuItem(tr("Перевіряю…"), "")
	status.Disable()
	systray.AddSeparator()
	open := systray.AddMenuItem(tr("Відкрити панель"), "")
	quit := systray.AddMenuItem(tr("Сховати значок"), tr("Друк продовжить працювати"))

	go func() {
		for {
			select {
			case <-open.ClickedCh:
				openPage()
			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		for {
			var st struct {
				Connected bool
				Name      string
				Error     string
			}
			text, icon := tr("Служба SlipDrukka не працює"), iconIdle
			if r, err := client.Get(panelURL() + "api/status"); err == nil {
				json.NewDecoder(r.Body).Decode(&st)
				r.Body.Close()
				switch {
				case st.Connected:
					text, icon = tr("На зв'язку: ")+st.Name, iconOK
				case st.Error != "":
					text, icon = tr("Немає зв'язку: ")+tr(st.Error), iconBad
				default:
					text, icon = tr("Немає зв'язку з сервером"), iconBad
				}
			}
			if len([]rune(text)) > 120 {
				text = string([]rune(text)[:120]) + "…"
			}
			status.SetTitle(text)
			systray.SetTooltip("SlipDrukka — " + text)
			systray.SetIcon(icon)
			time.Sleep(5 * time.Second)
		}
	}()
}
