// Package winpeer tells which Windows session the program at the other end
// of a local TCP connection belongs to.
//
// Services run in session 0; people who sign in, at the console or over
// Remote Desktop, get sessions 1 and up (Microsoft: "Session 0 isolation").
// The server uses this for two things on a computer that many people share:
//
//   - the local printer ports accept data only from session 0, that is from
//     the Windows print spooler. Measured on Windows Server 2019: when a
//     Windows printer prints to such a port, the connection belongs to
//     spoolsv.exe in session 0. A user's own program connecting to the port
//     directly would bypass the permissions of the Windows printer;
//   - failed sign-ins to the admin page are counted per session, so one
//     user cannot lock everybody else out.
package winpeer

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
)

// ErrUnavailable means Windows could not be asked at all (or this is not
// Windows). Callers treat it as "no answer", not as a refusal.
var ErrUnavailable = errors.New("відомості про з'єднання недоступні")

// ErrUnknown means the connection was not found among live connections, or
// has no owning process any more.
var ErrUnknown = errors.New("власника з'єднання не знайдено")

// ErrNoSession means the owning process is known but Windows did not tell
// which session it is in.
var ErrNoSession = errors.New("сеанс програми не визначено")

// Warn receives a message when the check cannot do its job on this
// computer (set by the server to its log). It is called at most once.
var Warn func(msg string)

var warned atomic.Bool

func tcpPort(a net.Addr) int {
	if t, ok := a.(*net.TCPAddr); ok {
		return t.Port
	}
	_, p, err := net.SplitHostPort(a.String())
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

// CheckPrintClient decides whether a connection to a local printer port may
// deliver a job: nil for the print spooler (session 0), an error with the
// reason for a program in a user's session. When Windows cannot be asked the
// job is accepted: printing must not stop over a failed check.
func CheckPrintClient(c net.Conn) error {
	session, pid, err := Session(tcpPort(c.RemoteAddr()), tcpPort(c.LocalAddr()))
	switch {
	case errors.Is(err, ErrUnavailable), errors.Is(err, ErrNoSession):
		// Windows gives no answer on this computer. Turning the spooler away
		// would stop all printing, so the job is accepted and the admin told.
		if Warn != nil && warned.CompareAndSwap(false, true) {
			Warn(fmt.Sprintf("не вдалося визначити, яка програма підключається до локальних портів принтерів (%v, процес %d) — приймаю завдання від будь-якої програми цього комп'ютера", err, pid))
		}
		return nil
	case err != nil:
		return err
	case session != 0:
		return fmt.Errorf("програма в сеансі користувача №%d (процес %d)", session, pid)
	}
	return nil
}

// LoginKey names who is trying to sign in to the admin page, for counting
// failed attempts: the Windows session for a browser on this computer, the
// address for anyone else.
func LoginKey(r *http.Request) string {
	host, port, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	ip := net.ParseIP(host)
	local, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if ip == nil || !ip.IsLoopback() || local == nil {
		return host
	}
	p, _ := strconv.Atoi(port)
	if session, _, err := Session(p, tcpPort(local)); err == nil {
		return "сеанс " + strconv.Itoa(int(session))
	}
	return host
}
