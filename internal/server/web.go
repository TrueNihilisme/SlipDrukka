package server

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"slipdrukka/internal/common"
	"slipdrukka/internal/i18n"
	"slipdrukka/internal/winauth"
)

//go:embed index.html
var indexHTML []byte

func (s *Server) routes() http.Handler {
	m := http.NewServeMux()
	g := s.sessions.Guard

	m.HandleFunc("GET /inter.woff2", common.FontHandler)
	m.HandleFunc("GET /ui-i18n.js", common.UIi18nHandler)
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(indexHTML)
	})
	m.HandleFunc("GET /api/hint", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		emergency := s.cfg.PasswordHash != ""
		s.mu.Unlock()
		common.JSON(w, map[string]bool{"windows": winauth.Supported(), "emergency": emergency, "local": isLoopback(r)})
	})
	m.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		who := s.loginKey(r)
		if ok, wait := s.limiter.Allow(who); !ok {
			common.JSONError(w, http.StatusTooManyRequests, fmt.Sprintf("забагато невдалих спроб — зачекайте %d с", int(wait.Seconds())+1))
			return
		}
		var in struct{ User, Password string }
		if common.ReadBody(r, &in) != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if strings.TrimSpace(in.User) == "" { // emergency panel password
			if s.sessions.Login(w, in.Password) {
				s.limiter.Reset(who)
				s.Log.Printf("вхід у панель аварійним паролем")
				s.record(r, "аварійний пароль", "вхід у панель", "")
				common.JSON(w, map[string]bool{"ok": true})
				return
			}
			s.limiter.Fail(who)
			s.Log.Printf("невдалий вхід у панель аварійним паролем (%s)", who)
			s.record(r, "аварійний пароль", "невдалий вхід", "")
			common.JSONError(w, http.StatusUnauthorized, "введіть ім'я та пароль адміністратора Windows")
			return
		}
		// A Windows password must not travel over the network in clear text:
		// only accept it on the server itself.
		if !isLoopback(r) {
			common.JSONError(w, http.StatusForbidden, "вхід обліковим записом Windows — лише на самому сервері (http://127.0.0.1)")
			return
		}
		if err := CheckLogin(in.User, in.Password); err != nil {
			s.limiter.Fail(who)
			s.Log.Printf("невдалий вхід у панель як «%s» (%s): %v", in.User, who, err)
			s.record(r, in.User, "невдалий вхід", err.Error())
			common.JSONError(w, http.StatusUnauthorized, err.Error())
			return
		}
		s.limiter.Reset(who)
		s.sessions.Grant(w, in.User)
		s.Log.Printf("вхід у панель: %s", in.User)
		s.record(r, in.User, "вхід у панель", "")
		common.JSON(w, map[string]bool{"ok": true})
	})
	m.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		if u := s.sessions.User(r); u != "" {
			s.record(r, u, "вихід з панелі", "")
		}
		s.sessions.Logout(w, r)
		common.JSON(w, map[string]bool{"ok": true})
	})
	m.HandleFunc("GET /api/state", g(func(w http.ResponseWriter, r *http.Request) {
		common.JSON(w, s.StateIn(common.LangOf(w)))
	}))
	m.HandleFunc("GET /api/firewall", g(func(w http.ResponseWriter, r *http.Request) {
		common.JSON(w, s.Firewall())
	}))
	m.HandleFunc("POST /api/lang", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Lang string }
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if err := s.SetLang(in.Lang); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "змінено мову журналів", in.Lang)
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("GET /api/drivers", g(func(w http.ResponseWriter, r *http.Request) {
		d, err := s.admin.Drivers()
		if err != nil {
			common.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		common.JSON(w, d)
	}))
	m.HandleFunc("POST /api/agents", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name string }
		if err := common.ReadBody(r, &in); err != nil || strings.TrimSpace(in.Name) == "" {
			common.JSONError(w, http.StatusBadRequest, "потрібна назва точки")
			return
		}
		id, code, err := s.AddAgent(in.Name)
		if err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "додано точку", in.Name)
		common.JSON(w, map[string]string{"id": id, "code": code})
	}))
	m.HandleFunc("POST /api/agents/{id}/rekey", g(func(w http.ResponseWriter, r *http.Request) {
		name := s.agentName(r.PathValue("id"))
		code, err := s.Rekey(r.PathValue("id"))
		if err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "видано новий код точки", name)
		common.JSON(w, map[string]string{"code": code})
	}))
	m.HandleFunc("DELETE /api/agents/{id}", g(func(w http.ResponseWriter, r *http.Request) {
		name := s.agentName(r.PathValue("id"))
		if err := s.DeleteAgent(r.PathValue("id")); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "видалено точку", name)
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /api/queue", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Agent, Alias, Driver, Queue string }
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if err := s.CreateQueue(in.Agent, in.Alias, in.Driver, in.Queue); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "створено принтер Windows", in.Queue+" → "+s.agentName(in.Agent)+" / "+in.Alias)
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /api/settings", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			PublicAddr string `json:"public_addr"`
			JobTTL     int    `json:"job_ttl_minutes"`
		}
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if err := s.SetPublicAddr(in.PublicAddr); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if in.JobTTL != 0 {
			if err := s.SetJobTTL(in.JobTTL); err != nil {
				common.JSONError(w, http.StatusBadRequest, "термін зберігання: "+err.Error())
				return
			}
		}
		s.record(r, "", "змінено налаштування", fmt.Sprintf("адреса %s, черга %d хв", in.PublicAddr, in.JobTTL))
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /api/ports", g(func(w http.ResponseWriter, r *http.Request) {
		var in Ports
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if err := s.SetPorts(in); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "змінено порти", fmt.Sprintf("точки %d, панель %d (мережа: %v), принтери від %d", in.AgentPort, in.WebPort, in.WebLAN, in.FirstPort))
		common.JSON(w, s.ports())
	}))
	m.HandleFunc("POST /api/allowed", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Allowed string
			Force   bool
		}
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if err := s.SetAllowed(in.Allowed, in.Force); err != nil {
			var wd *WouldDropError
			if errors.As(err, &wd) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]any{"error": i18n.Tr(common.LangOf(w), err.Error()), "would_drop": wd.Points})
				return
			}
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "змінено список дозволених адрес", strings.Join(strings.Fields(in.Allowed), ", "))
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /api/agents/{id}/allowed", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Allowed string }
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if err := s.SetAgentAllowed(r.PathValue("id"), in.Allowed); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "прив'язано точку до адрес", s.agentName(r.PathValue("id"))+": "+strings.Join(strings.Fields(in.Allowed), ", "))
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /api/agents/{id}/cmd", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Cmd  string
			Args json.RawMessage
		}
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if len(in.Args) == 0 {
			in.Args = json.RawMessage(`{}`)
		}
		data, err := s.Command(r.PathValue("id"), in.Cmd, in.Args)
		if err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if in.Cmd != "test_port" && in.Cmd != "drivers" { // read-only ones are not logged
			s.record(r, "", "команда точці", s.agentName(r.PathValue("id"))+": "+in.Cmd)
		}
		common.JSON(w, map[string]any{"ok": true, "data": data})
	}))
	m.HandleFunc("POST /api/agents/{id}/free", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Alias string }
		if err := common.ReadBody(r, &in); err != nil || in.Alias == "" {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if err := s.FreePort(r.PathValue("id"), in.Alias); err != nil {
			common.JSONError(w, http.StatusConflict, err.Error())
			return
		}
		s.record(r, "", "звільнено порт", s.agentName(r.PathValue("id"))+" / "+in.Alias)
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /api/agents/{id}/rename", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name string }
		if err := common.ReadBody(r, &in); err != nil || strings.TrimSpace(in.Name) == "" {
			common.JSONError(w, http.StatusBadRequest, "потрібна назва")
			return
		}
		old := s.agentName(r.PathValue("id"))
		if err := s.RenameAgent(r.PathValue("id"), in.Name); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "перейменовано точку", old+" → "+in.Name)
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /api/queue/rename", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Old, New string }
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if err := s.RenameQueue(in.Old, in.New); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "перейменовано принтер Windows", in.Old+" → "+in.New)
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("DELETE /api/jobs/{id}", g(func(w http.ResponseWriter, r *http.Request) {
		if err := s.CancelJob(r.PathValue("id")); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "скасовано чек у черзі", r.PathValue("id"))
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /api/password", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Old, New string }
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		s.mu.Lock()
		okOld := common.CheckPassword(s.cfg.PasswordHash, in.Old)
		s.mu.Unlock()
		if !okOld {
			common.JSONError(w, http.StatusBadRequest, "старий пароль невірний")
			return
		}
		if len(in.New) < 8 {
			common.JSONError(w, http.StatusBadRequest, "новий пароль — щонайменше 8 символів")
			return
		}
		if err := s.SetPassword(in.New); err != nil {
			common.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.record(r, "", "змінено аварійний пароль", "")
		s.sessions.DropAll()
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /api/security", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			BanHours    int  `json:"ban_hours"`
			CodeTTLDays int  `json:"code_ttl_days"`
			AutoPin     bool `json:"auto_pin"`
		}
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if err := s.SetSecurity(in.BanHours, in.CodeTTLDays, in.AutoPin); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		pin := "ні"
		if in.AutoPin {
			pin = "так"
		}
		s.record(r, "", "змінено захист", fmt.Sprintf("блокування %s, код діє %s, прив'язка до першої адреси: %s",
			hoursText(in.BanHours), daysText(in.CodeTTLDays), pin))
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /api/wg", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Mode string }
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if err := s.SetWGMode(in.Mode); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "змінено канал WireGuard", wgModeText(in.Mode))
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /api/crowdsec", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Log bool   `json:"log"`
			Ask bool   `json:"ask"`
			URL string `json:"url"`
			Key string `json:"key"`
		}
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if err := s.SetCrowdSec(in.Log, in.Ask, in.URL, in.Key); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		detail := fmt.Sprintf("журнал для CrowdSec: %s, питати CrowdSec: %s", yesNo(in.Log), yesNo(in.Ask))
		if in.Key != "" {
			detail += ", ключ API замінено"
		}
		s.record(r, "", "змінено CrowdSec", detail)
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /api/bans/remove", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ IP string }
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		if err := s.Unban(in.IP); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "розблоковано адресу", in.IP)
		common.JSON(w, map[string]bool{"ok": true})
	}))
	m.HandleFunc("POST /api/alerts/dismiss", g(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ ID string }
		if err := common.ReadBody(r, &in); err != nil {
			common.JSONError(w, http.StatusBadRequest, "некоректний запит")
			return
		}
		detail := s.alertText(in.ID)
		if err := s.DismissAlert(in.ID); err != nil {
			common.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.record(r, "", "прийнято нову адресу точки", detail)
		common.JSON(w, map[string]bool{"ok": true})
	}))
	return m
}

func (s *Server) agentName(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentNameLocked(id)
}

func (s *Server) alertText(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.alertByIDLocked(id); a != nil {
		return a.Name + ": " + a.IP
	}
	return id
}

func hoursText(h int) string {
	switch {
	case h == foreverSetting:
		return "назавжди"
	case h%24 == 0:
		return fmt.Sprintf("%d д", h/24)
	}
	return fmt.Sprintf("%d год", h)
}

func daysText(d int) string {
	if d == foreverSetting {
		return "без терміну"
	}
	return fmt.Sprintf("%d д", d)
}

// loginKey names who is trying to sign in, for counting failed attempts.
func (s *Server) loginKey(r *http.Request) string {
	if s.LoginKey != nil {
		return s.LoginKey(r)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func yesNo(b bool) string {
	if b {
		return "так"
	}
	return "ні"
}
