//go:build windows

// slipdrukka-tray is the icon next to the clock. It only shows the agent's
// state and opens its page; the real work is done by the agent service.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os/exec"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

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
	// One icon per user session.
	name, _ := windows.UTF16PtrFromString(`Local\SlipDrukkaTray`)
	if _, err := windows.CreateMutex(nil, false, name); err == windows.ERROR_ALREADY_EXISTS {
		return
	}
	systray.Run(onReady, func() {})
}

var (
	iconOK   = makeIcon(color.RGBA{22, 163, 74, 255})
	iconBad  = makeIcon(color.RGBA{220, 38, 38, 255})
	iconIdle = makeIcon(color.RGBA{120, 128, 140, 255})
)

func onReady() {
	systray.SetIcon(iconIdle)
	systray.SetTooltip("SlipDrukka")
	status := systray.AddMenuItem("Перевіряю…", "")
	status.Disable()
	systray.AddSeparator()
	open := systray.AddMenuItem("Відкрити панель", "")
	quit := systray.AddMenuItem("Сховати значок", "Друк продовжить працювати")

	go func() {
		for {
			select {
			case <-open.ClickedCh:
				exec.Command(winsys.Exe("rundll32.exe"), "url.dll,FileProtocolHandler", panelURL()).Start()
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
			text, icon := "Служба SlipDrukka не працює", iconBad
			if r, err := client.Get(panelURL() + "api/status"); err == nil {
				json.NewDecoder(r.Body).Decode(&st)
				r.Body.Close()
				switch {
				case st.Connected:
					text, icon = "На зв'язку: "+st.Name, iconOK
				case st.Error != "":
					text, icon = "Немає зв'язку: "+st.Error, iconBad
				default:
					text, icon = "Немає зв'язку з сервером", iconBad
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

// makeIcon draws a filled printer-ish square with a status colour and wraps
// the PNG into an .ico container, which is what the Windows tray expects.
func makeIcon(c color.RGBA) []byte {
	const n = 32
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			inBody := x >= 3 && x <= 28 && y >= 10 && y <= 23
			inPaperTop := x >= 8 && x <= 23 && y >= 3 && y <= 10
			inPaperOut := x >= 8 && x <= 23 && y >= 23 && y <= 29
			switch {
			case inBody:
				img.Set(x, y, c)
			case inPaperTop || inPaperOut:
				img.Set(x, y, color.RGBA{245, 245, 245, 255})
			}
		}
	}
	var p bytes.Buffer
	png.Encode(&p, img)
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, []uint16{0, 1, 1})
	ico.Write([]byte{n, n, 0, 0})
	binary.Write(&ico, binary.LittleEndian, []uint16{1, 32})
	binary.Write(&ico, binary.LittleEndian, []uint32{uint32(p.Len()), 22})
	ico.Write(p.Bytes())
	return ico.Bytes()
}
