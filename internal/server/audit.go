package server

// Admin action log: who did what in the panel, from which address. One JSON
// line per action in audit.log next to the config (rotated to audit.log.1 at
// auditMaxBytes); the latest auditKeep entries are also kept in memory for
// the panel.

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"slipdrukka/internal/common"
)

const (
	auditMaxBytes = 1 << 20
	auditKeep     = 200
)

type AuditEntry struct {
	At     time.Time `json:"at"`
	Who    string    `json:"who"`
	From   string    `json:"from"`
	Action string    `json:"action"`
	Detail string    `json:"detail,omitempty"`
}

type auditLog struct {
	mu   sync.Mutex
	path string
	last []AuditEntry // oldest first
}

func openAudit(dir string) *auditLog {
	l := &auditLog{path: common.Join(dir, "audit.log")}
	if f, err := os.Open(l.path); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var e AuditEntry
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				l.last = append(l.last, e)
				if len(l.last) > auditKeep {
					l.last = l.last[1:]
				}
			}
		}
		f.Close()
	}
	return l
}

func (l *auditLog) add(e AuditEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last = append(l.last, e)
	if len(l.last) > auditKeep {
		l.last = l.last[len(l.last)-auditKeep:]
	}
	if st, err := os.Stat(l.path); err == nil && st.Size() > auditMaxBytes {
		os.Rename(l.path, l.path+".1")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	return err
}

// recent returns the latest n entries, newest first.
func (l *auditLog) recent(n int) []AuditEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []AuditEntry
	for i := len(l.last) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, l.last[i])
	}
	return out
}

// record writes one admin action. who: the signed-in user, or override when
// there is no session yet (sign-in attempts).
func (s *Server) record(r *http.Request, who, action, detail string) {
	if who == "" {
		who = s.sessions.User(r)
	}
	from, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		from = r.RemoteAddr
	}
	if err := s.audit.add(AuditEntry{At: time.Now(), Who: who, From: from, Action: action, Detail: detail}); err != nil {
		s.Log.Printf("ПОМИЛКА журналу дій: %v", err)
	}
}
