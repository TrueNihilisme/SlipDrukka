// Package common has the small pieces both programs share: JSON config
// files, the log ring, passwords and web sessions.
package common

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slipdrukka/internal/i18n"
	"strings"
	"sync"
	"time"
)

// LoadJSON reads path into v. A missing file is not an error.
func LoadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// SaveJSON writes v to path atomically (temp file + rename).
func SaveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// The data reaches the disk before the rename: after a power cut the file
	// is either the old one or the complete new one (queued receipts).
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RandomToken returns n random bytes as URL-safe text.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// RandomPassword is easy to read aloud: no 0/O, 1/l/I.
func RandomPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:4]) + "-" + string(b[4:8]) + "-" + string(b[8:])
}

// SHA256Hex is used to store agent keys without keeping them in clear.
func SHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

const pbkdfIter = 200_000

// HashPassword returns "pbkdf2$<salt>$<hash>".
func HashPassword(pw string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	h, err := pbkdf2.Key(sha256.New, pw, salt, pbkdfIter, 32)
	if err != nil {
		panic(err)
	}
	return "pbkdf2$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(h)
}

func CheckPassword(stored, pw string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 || parts[0] != "pbkdf2" {
		return false
	}
	salt, err1 := hex.DecodeString(parts[1])
	want, err2 := hex.DecodeString(parts[2])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, pbkdfIter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ---------------------------------------------------------------- log ring

// LogRing keeps the last lines in memory for the web page and appends every
// line to a file that is cut in half when it grows past maxFile.
type LogRing struct {
	mu    sync.Mutex
	lines []string // kept in the program's own language (Ukrainian)
	max   int
	path  string
	// Lang gives the language of the log file (nil: Ukrainian). The panels
	// translate the lines for each viewer themselves (LinesIn).
	Lang func() string
}

const maxFile = 5 << 20

func NewLogRing(path string, max int) *LogRing {
	return &LogRing{path: path, max: max}
}

func (l *LogRing) Printf(format string, args ...any) {
	stamp := time.Now().Format("2006-01-02 15:04:05 ")
	msg := fmt.Sprintf(format, args...)
	line := stamp + msg
	if l.Lang != nil {
		msg = i18n.Tr(l.Lang(), msg)
	}
	out := stamp + msg
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
	if len(l.lines) > l.max {
		l.lines = l.lines[len(l.lines)-l.max:]
	}
	if l.path == "" {
		fmt.Println(out)
		return
	}
	if st, err := os.Stat(l.path); err == nil && st.Size() > maxFile {
		if b, err := os.ReadFile(l.path); err == nil {
			_ = os.WriteFile(l.path, b[len(b)/2:], 0o600)
		}
	}
	if f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		io.WriteString(f, out+"\r\n")
		f.Close()
	}
}

// LinesIn is Lines translated into lang (the time stamp is kept as is).
func (l *LogRing) LinesIn(lang string) []string {
	lines := l.Lines()
	for i, s := range lines {
		if len(s) > 20 {
			lines[i] = s[:20] + i18n.Tr(lang, s[20:])
		}
	}
	return lines
}

func (l *LogRing) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.lines))
	// newest first
	for i, s := range l.lines {
		out[len(l.lines)-1-i] = s
	}
	return out
}

// ---------------------------------------------------------------- web auth

// Sessions is a tiny cookie session store for one admin password.
type Sessions struct {
	mu       sync.Mutex
	tokens   map[string]session
	cookie   string
	password func() string // returns the stored hash
}

func NewSessions(cookieName string, passwordHash func() string) *Sessions {
	return &Sessions{tokens: map[string]session{}, cookie: cookieName, password: passwordHash}
}

// session: when it ends and who signed in (for the admin action log).
type session struct {
	exp  time.Time
	user string
}

const sessionTTL = 12 * time.Hour

func (s *Sessions) valid(r *http.Request) bool {
	c, err := r.Cookie(s.cookie)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.tokens[c.Value]
	if !ok || time.Now().After(st.exp) {
		delete(s.tokens, c.Value)
		return false
	}
	st.exp = time.Now().Add(sessionTTL)
	s.tokens[c.Value] = st
	return true
}

// User names who owns the request's session ("" without one).
func (s *Sessions) User(r *http.Request) string {
	c, err := r.Cookie(s.cookie)
	if err != nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[c.Value].user
}

// Login checks the password and sets the session cookie.
func (s *Sessions) Login(w http.ResponseWriter, pw string) bool {
	if s.password() == "" || !CheckPassword(s.password(), pw) {
		time.Sleep(700 * time.Millisecond) // slow down guessing
		return false
	}
	s.Grant(w, "аварійний пароль")
	return true
}

// Grant starts a session for user after the caller verified them otherwise.
func (s *Sessions) Grant(w http.ResponseWriter, user string) {
	tok := RandomToken(24)
	s.mu.Lock()
	s.tokens[tok] = session{exp: time.Now().Add(sessionTTL), user: user}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: s.cookie, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func (s *Sessions) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cookie); err == nil {
		s.mu.Lock()
		delete(s.tokens, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookie, Value: "", Path: "/", MaxAge: -1})
}

// DropAll ends every session (after a password change).
func (s *Sessions) DropAll() {
	s.mu.Lock()
	s.tokens = map[string]session{}
	s.mu.Unlock()
}

// Guard protects API handlers: valid session plus a custom header, which a
// cross-site form cannot send (cheap CSRF protection).
func (s *Sessions) Guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get("X-SD") != "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !s.valid(r) {
			JSONError(w, http.StatusUnauthorized, "потрібен вхід")
			return
		}
		h(w, r)
	}
}

func (s *Sessions) LoggedIn(r *http.Request) bool { return s.valid(r) }

// ---------------------------------------------------------------- http helpers

func JSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// LangWriter carries the viewer's language down to JSONError.
type LangWriter struct {
	http.ResponseWriter
	Lang string
}

// WithLang reads the viewer's language (X-Lang from the panels, otherwise
// Accept-Language) and hands it to the handler through the writer.
func WithLang(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := r.Header.Get("X-Lang")
		if lang == "" {
			lang = r.Header.Get("Accept-Language")
		}
		l := i18n.UK // the program's own language when nothing is said
		if lang != "" {
			l = i18n.Normalize(lang)
		}
		h.ServeHTTP(&LangWriter{ResponseWriter: w, Lang: l}, r)
	})
}

// LangOf is the viewer's language for a writer from WithLang (else UK).
func LangOf(w http.ResponseWriter) string {
	if lw, ok := w.(*LangWriter); ok {
		return lw.Lang
	}
	return i18n.UK
}

func JSONError(w http.ResponseWriter, code int, msg string) {
	msg = i18n.Tr(LangOf(w), msg)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func ReadBody(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

// HostAllowed: the panels are opened by IP address, "localhost" or this
// computer's own name. Any other name is refused: that is what a
// DNS-rebinding attack from a random web page would send.
func HostAllowed(host string) bool {
	if hh, _, err := net.SplitHostPort(host); err == nil {
		host = hh
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "localhost" || net.ParseIP(host) != nil {
		return true
	}
	me, err := os.Hostname()
	if err != nil || me == "" {
		return false
	}
	me = strings.ToLower(me)
	short, _, _ := strings.Cut(me, ".")
	return host == me || host == short
}

// SecurityHeaders keeps the admin pages from being framed or sniffed and
// refuses unexpected Host names (see HostAllowed).
func SecurityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !HostAllowed(r.Host) {
			http.Error(w, "Відкрийте цю сторінку за IP-адресою, через localhost або за ім'ям цього комп'ютера", http.StatusMisdirectedRequest)
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// Only this page's own scripts, styles, font and API: nothing is loaded
		// from or sent to another site. Inline scripts stay allowed: the pages
		// are single files with inline handlers.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.ServeHTTP(w, r)
	})
}

// EnsureDir creates dir (and parents).
func EnsureDir(dir string) error { return os.MkdirAll(dir, 0o700) }

// Join is filepath.Join, re-exported for brevity in callers.
func Join(p ...string) string { return filepath.Join(p...) }

// Limiter slows down password guessing, separately for every key (who is
// trying: a Windows session or an address). After max failures within window
// the key is refused for block; every further block for the same key is twice
// as long, up to maxBlock. Other keys are not affected, so one person cannot
// lock everybody else out.
type Limiter struct {
	mu       sync.Mutex
	keys     map[string]*limiterState
	max      int
	window   time.Duration
	blockFor time.Duration
	maxBlock time.Duration
}

type limiterState struct {
	fails  []time.Time
	until  time.Time
	blocks int
	last   time.Time
}

func NewLimiter(max int, window, block time.Duration) *Limiter {
	return &Limiter{keys: map[string]*limiterState{}, max: max, window: window, blockFor: block, maxBlock: 15 * time.Minute}
}

// Allow reports whether an attempt by key may proceed and, if not, for how long.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if st := l.keys[key]; st != nil {
		if d := time.Until(st.until); d > 0 {
			return false, d
		}
	}
	return true, 0
}

// Fail records a failed attempt by key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.keys) > 1000 { // keep memory bounded: forget keys not seen for an hour
		for k, st := range l.keys {
			if now.Sub(st.last) > time.Hour {
				delete(l.keys, k)
			}
		}
	}
	st := l.keys[key]
	if st == nil {
		st = &limiterState{}
		l.keys[key] = st
	}
	st.last = now
	keep := st.fails[:0]
	for _, t := range st.fails {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	st.fails = append(keep, now)
	if len(st.fails) >= l.max {
		block := l.blockFor << st.blocks
		if block > l.maxBlock || block <= 0 {
			block = l.maxBlock
		}
		st.until = now.Add(block)
		st.blocks++
		st.fails = nil
	}
}

// Reset forgets key after a successful sign-in.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.keys, key)
	l.mu.Unlock()
}
