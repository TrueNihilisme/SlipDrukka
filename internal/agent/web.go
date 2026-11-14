package agent

// The point's own page only shows state. Everything is configured from the
// server panel, so there is no password here and nothing a shop employee
// could break. The one action is entering the connection code, and only for
// a new point or after the server refused the current code.

import (
	"context"
	_ "embed"
	"net/http"
	"time"

	"slipdrukka/internal/common"
	"slipdrukka/internal/i18n"
	"slipdrukka/internal/proto"
)

//go:embed index.html
var indexHTML []byte

type StateView struct {
	Status   Status          `json:"status"`
	HasCode  bool            `json:"has_code"`
	CodeOK   bool            `json:"code_replaceable"`
	Shared   []proto.Printer `json:"shared"`
	Events   []proto.Event   `json:"events"`
	JobsOK   int             `json:"jobs_ok"`
	JobsFail int             `json:"jobs_failed"`
	Log      []string        `json:"log"`
	Warning  string          `json:"warning,omitempty"` // see SetWarning
}

// SetWarning sets (or, with "", clears) the warning shown at the top of the
// page: a problem of the service itself (it runs with LocalSystem rights
// although it should not).
func (a *Agent) SetWarning(text string) {
	a.mu.Lock()
	a.warning = text
	a.mu.Unlock()
}

func (a *Agent) State() StateView {
	codeOK := a.CodeReplaceable()
	a.mu.Lock()
	defer a.mu.Unlock()
	v := StateView{Status: a.status, HasCode: a.cfg.Code != "", JobsOK: a.jobs.OK, JobsFail: a.jobs.Failed,
		Shared: append([]proto.Printer(nil), a.cfg.Shared...), Events: append([]proto.Event(nil), a.events...), Log: a.Log.Lines()}
	v.Status.Version = a.Version
	v.Warning = a.warning
	v.CodeOK = codeOK
	return v
}

func (a *Agent) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /inter.woff2", common.FontHandler)
	m.HandleFunc("GET /ui-i18n.js", common.UIi18nHandler)
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(indexHTML)
	})
	// The tray icon polls this.
	m.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		st := a.Status()
		common.JSON(w, map[string]any{"connected": st.Connected, "name": st.Name, "error": st.Error, "version": st.Version})
	})
	m.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		common.JSON(w, a.StateIn(common.LangOf(w)))
	})
	m.HandleFunc("POST /api/code", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-SD") != "1" { // a cross-site form cannot set it
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// Without a password here, only allow a code when there is none yet or
		// the server refused the current one (point deleted / code re-issued).
		// A shop employee therefore cannot move a working point elsewhere.
		if !a.CodeReplaceable() {
			common.JSONError(w, http.StatusConflict, "код можна вписати лише для нової точки або коли сервер відхилив поточний код. Новий код видає адміністратор у панелі сервера")
			return
		}
		var in struct{ Code string }
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if in.Code == "" {
			common.JSONError(w, http.StatusBadRequest, "вставте код")
			return
		}
		if err := a.SetCode(in.Code); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		common.JSON(w, map[string]bool{"ok": true})
	})
	// Network tab (0.17.0): this computer's addresses and the route to the server.
	m.HandleFunc("GET /api/net", func(w http.ResponseWriter, r *http.Request) {
		common.JSON(w, a.netInfo())
	})
	m.HandleFunc("POST /api/trace", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-SD") != "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 80*time.Second)
		defer cancel()
		data, err := a.traceToServer(ctx)
		if err != nil {
			common.JSONError(w, http.StatusBadRequest, i18n.Tr(common.LangOf(w), err.Error()))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	})
	return m
}

// StateIn is State translated into lang for the page's viewer.
func (a *Agent) StateIn(lang string) StateView {
	v := a.State()
	if lang != i18n.EN {
		return v
	}
	v.Status.Error = i18n.Tr(lang, v.Status.Error)
	v.Warning = i18n.Tr(lang, v.Warning)
	for i := range v.Events {
		v.Events[i].Text = i18n.Tr(lang, v.Events[i].Text)
	}
	v.Log = a.Log.LinesIn(lang)
	return v
}
