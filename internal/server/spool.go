package server

// Store-and-forward. Every job Windows sends to a local port is first written
// to disk and only then acknowledged to Windows. A per-point worker delivers
// queued jobs in order and deletes a job only after the agent confirms it was
// handed to its spooler. Undelivered jobs survive restarts and lost links;
// jobs older than the TTL are dropped so yesterday's receipts don't come out
// in the morning. The agent remembers recent job IDs, so a job re-sent after
// a lost confirmation is not printed twice.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
)

const (
	defaultTTL  = 60 * time.Minute
	maxJobBytes = 1 << 30
	retryDelay  = 10 * time.Second
	pingTimeout = 5 * time.Second
)

// MaxSpoolBytes limits all queued jobs together. The local printer ports
// accept data from any program on this computer, so on a terminal server
// any user could otherwise fill the disk with jobs for a shop that is
// offline. When the limit is reached new jobs are refused (Windows keeps
// them in its own queue and retries) until queued ones are delivered or
// expire.
var MaxSpoolBytes int64 = 4 << 30

type spoolJob struct {
	ID        string    `json:"id"`
	Seq       int64     `json:"seq"`
	AgentID   string    `json:"agent"`
	Alias     string    `json:"alias"`
	Created   time.Time `json:"created"`
	Bytes     int64     `json:"bytes"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error"`
}

func (s *Server) spoolDir() string { return filepath.Join(s.dir, "spool") }
func (s *Server) jobData(id string) string {
	return filepath.Join(s.spoolDir(), id+".bin")
}
func (s *Server) jobMeta(id string) string {
	return filepath.Join(s.spoolDir(), id+".json")
}

func (s *Server) ttl() time.Duration {
	if s.cfg.JobTTLMinutes > 0 {
		return time.Duration(s.cfg.JobTTLMinutes) * time.Minute
	}
	return defaultTTL
}

// loadSpool picks up jobs left from a previous run. s.mu held.
func (s *Server) loadSpoolLocked() error {
	if err := os.MkdirAll(s.spoolDir(), 0o700); err != nil {
		return err
	}
	metas, _ := filepath.Glob(filepath.Join(s.spoolDir(), "*.json"))
	for _, m := range metas {
		var j spoolJob
		if common.LoadJSON(m, &j) != nil || j.ID == "" {
			continue
		}
		if _, err := os.Stat(s.jobData(j.ID)); err != nil {
			os.Remove(m)
			continue
		}
		s.jobs[j.ID] = &j
		if j.Seq > s.seq {
			s.seq = j.Seq
		}
	}
	// half-written data files without metadata
	datas, _ := filepath.Glob(filepath.Join(s.spoolDir(), "*.bin"))
	for _, d := range datas {
		id := strings.TrimSuffix(filepath.Base(d), ".bin")
		if s.jobs[id] == nil {
			os.Remove(d)
		}
	}
	if n := len(s.jobs); n > 0 {
		s.Log.Printf("у черзі з минулого запуску: %d завдань", n)
	}
	return nil
}

// ArrivingQuiet: an arriving job that has sent nothing for this long no
// longer holds back the later jobs of its printer (a stuck connection must
// not stop the till). It may still finish, up to the read deadline.
var ArrivingQuiet = 30 * time.Second

// startIncoming gives a new job its order number and marks it as arriving.
func (s *Server) startIncoming(key string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	if s.arriving == nil {
		s.arriving = map[string]map[int64]time.Time{}
	}
	if s.arriving[key] == nil {
		s.arriving[key] = map[int64]time.Time{}
	}
	s.arriving[key][s.seq] = time.Now()
	return s.seq
}

// holdsBackLocked: an earlier job for the same printer is still arriving, so
// job seq must wait to keep the order. s.mu held.
func (s *Server) holdsBackLocked(key string, seq int64) bool {
	for earlier, last := range s.arriving[key] {
		if earlier < seq && time.Since(last) < ArrivingQuiet {
			return true
		}
	}
	return false
}

// acceptJob reads one job from Windows into the spool. Windows gets a clean
// close only once the job is safely on disk; on any read error the
// connection is reset, so Windows reports an error and retries the job.
func (s *Server) acceptJob(key string, local net.Conn, seq int64) {
	defer local.Close()
	agentID, alias, _ := strings.Cut(key, "/")
	defer func() { // received or failed: later jobs of this printer may go
		s.mu.Lock()
		delete(s.arriving[key], seq)
		s.mu.Unlock()
		s.wake(agentID)
	}()
	reset := func() {
		if tc, ok := local.(*net.TCPConn); ok {
			tc.SetLinger(0)
		}
	}

	s.mu.Lock()
	j := &spoolJob{ID: common.RandomToken(9), Seq: seq, AgentID: agentID, Alias: alias, Created: time.Now()}
	name := s.agentNameLocked(agentID)
	var queued int64
	for _, q := range s.jobs {
		queued += q.Bytes
	}
	s.mu.Unlock()
	defer func() { s.incoming.Add(-j.Bytes) }()

	f, err := os.OpenFile(s.jobData(j.ID), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		s.Log.Printf("друк %s/%s: ПОМИЛКА запису в чергу: %v", name, alias, err)
		reset()
		return
	}
	buf := make([]byte, 64*1024)
	var rerr error
	for {
		local.SetReadDeadline(time.Now().Add(5 * time.Minute))
		n, err := local.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				rerr = werr
				break
			}
			j.Bytes += int64(n)
			s.mu.Lock()
			if m := s.arriving[key]; m != nil {
				m[seq] = time.Now()
			}
			s.mu.Unlock()
			receiving := s.incoming.Add(int64(n)) // all jobs being received now, this one included
			if j.Bytes > maxJobBytes {
				rerr = errors.New("завдання більше 1 ГБ")
				break
			}
			if queued+receiving > MaxSpoolBytes {
				rerr = fmt.Errorf("черга сервера заповнена: понад %d МБ чекає доставки", MaxSpoolBytes>>20)
				break
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			rerr = err
			break
		}
	}
	if rerr == nil {
		rerr = f.Sync()
	}
	f.Close()
	if rerr == nil {
		rerr = common.SaveJSON(s.jobMeta(j.ID), j)
	}
	if rerr != nil {
		os.Remove(s.jobData(j.ID))
		os.Remove(s.jobMeta(j.ID))
		reset()
		s.Log.Printf("друк %s/%s: не прийнято від Windows (%v) — Windows повторить", name, alias, rerr)
		return
	}

	s.mu.Lock()
	s.jobs[j.ID] = j
	s.mu.Unlock()
	s.Log.Printf("друк %s/%s: прийнято %d байт у чергу (завдання %s)", name, alias, j.Bytes, j.ID)
	s.wake(agentID)
}

func (s *Server) agentNameLocked(id string) string {
	if a := s.findAgentLocked(id); a != nil {
		return a.Name
	}
	return id
}

// wake makes sure a delivery worker for the point is running and nudges it.
func (s *Server) wake(agentID string) {
	s.mu.Lock()
	w, ok := s.workers[agentID]
	if !ok {
		w = &deliverWorker{wake: make(chan struct{}, 1), stop: make(chan struct{})}
		s.workers[agentID] = w
		go s.deliverLoop(s.ctx, agentID, w)
	}
	s.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// deliverWorker: wake nudges the point's delivery loop, stop (closed when
// the point is deleted) ends it.
type deliverWorker struct{ wake, stop chan struct{} }

func (s *Server) deliverLoop(ctx context.Context, agentID string, w *deliverWorker) {
	tick := time.NewTicker(retryDelay)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-w.wake:
		case <-tick.C:
		}
		s.deliverPending(agentID)
	}
}

// pendingLocked returns the point's jobs in arrival order. s.mu held.
func (s *Server) pendingLocked(agentID string) []*spoolJob {
	var out []*spoolJob
	for _, j := range s.jobs {
		if j.AgentID == agentID {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Seq < out[b].Seq })
	return out
}

// deliverPending sends queued jobs of one point. Order is kept per printer;
// a printer that is not offered right now or failed in this round does not
// hold back the other printers of the same point.
func (s *Server) deliverPending(agentID string) {
	for {
		s.mu.Lock()
		jobs := s.pendingLocked(agentID)
		conn := s.online[agentID]
		// The list is replaced whenever the point reports (applyInventory):
		// take it under the lock.
		var offered []proto.Printer
		if conn != nil {
			offered = conn.printers
		}
		ttl := s.ttl()
		name := s.agentNameLocked(agentID)
		s.mu.Unlock()

		progressed, pinged := false, false
		blocked := map[string]bool{}
		for _, j := range jobs {
			if blocked[j.Alias] {
				continue
			}
			s.mu.Lock()
			held := s.holdsBackLocked(portKey(agentID, j.Alias), j.Seq)
			s.mu.Unlock()
			if held {
				blocked[j.Alias] = true // an earlier job of this printer is still arriving
				continue
			}
			if time.Since(j.Created) > ttl {
				s.dropJob(j, fmt.Sprintf("старше %s — не доставлено", ttl))
				progressed = true
				continue
			}
			if conn == nil {
				return // wait for the point to come back
			}
			target := ""
			for _, p := range offered {
				if p.Alias == j.Alias {
					target = p.Name
				}
			}
			if target == "" {
				blocked[j.Alias] = true // not offered now; keep until TTL
				continue
			}
			if !pinged {
				if err := ping(conn); err != nil {
					s.Log.Printf("точка «%s» не відповідає на перевірку зв'язку — розриваю, чекаю повернення", name)
					conn.sess.Close()
					return
				}
				pinged = true
			}
			if err := s.sendJob(conn, j, target); err != nil {
				s.mu.Lock()
				j.Attempts++
				j.LastError = err.Error()
				common.SaveJSON(s.jobMeta(j.ID), j)
				s.statsLocked(portKey(agentID, j.Alias)).LastError = err.Error()
				s.mu.Unlock()
				s.Log.Printf("друк %s/%s: спроба %d не вдалася: %v — повторю", name, j.Alias, j.Attempts, err)
				blocked[j.Alias] = true
				continue
			}
			s.mu.Lock()
			delete(s.jobs, j.ID)
			st := s.statsLocked(portKey(agentID, j.Alias))
			st.OK++
			st.LastJob = time.Now()
			st.LastBytes = j.Bytes
			st.LastError = ""
			s.mu.Unlock()
			os.Remove(s.jobData(j.ID))
			os.Remove(s.jobMeta(j.ID))
			wait := time.Since(j.Created).Round(time.Second)
			s.Log.Printf("друк %s/%s → «%s»: %d байт надруковано (завдання %s, у черзі %s)", name, j.Alias, target, j.Bytes, j.ID, wait)
			progressed = true
		}
		if !progressed {
			return
		}
	}
}

func ping(conn *agentConn) error {
	done := make(chan error, 1)
	go func() { _, err := conn.sess.Ping(); done <- err }()
	select {
	case err := <-done:
		return err
	case <-time.After(pingTimeout):
		return errors.New("немає відповіді")
	}
}

// frameWait: how long one frame of a job (at most proto.MaxFrame bytes) may
// take to leave for the shop.
const frameWait = 2 * time.Minute

// sendJob streams one spooled job and waits for the agent's confirmation.
func (s *Server) sendJob(conn *agentConn, j *spoolJob, target string) error {
	f, err := os.Open(s.jobData(j.ID))
	if err != nil {
		return err
	}
	defer f.Close()
	stream, err := conn.sess.Open()
	if err != nil {
		return fmt.Errorf("канал: %w", err)
	}
	defer stream.Close()
	stream.SetWriteDeadline(time.Now().Add(frameWait))
	s.mu.Lock()
	keep := int(s.ttl() / time.Minute)
	s.mu.Unlock()
	if err := proto.WriteLine(stream, proto.JobHeader{ID: j.ID, Printer: target, KeepMin: keep}); err != nil {
		return err
	}
	buf := make([]byte, proto.MaxFrame)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			// Per frame, not for the whole job: a large document over a slow
			// line must be able to arrive, a stalled line must still give up.
			stream.SetWriteDeadline(time.Now().Add(frameWait))
			if err := proto.WriteFrame(stream, buf[:n]); err != nil {
				return fmt.Errorf("передача: %w", err)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	stream.SetWriteDeadline(time.Now().Add(frameWait))
	if err := proto.WriteFrame(stream, nil); err != nil {
		return fmt.Errorf("передача: %w", err)
	}
	stream.SetReadDeadline(time.Now().Add(2 * time.Minute))
	var res proto.JobResult
	if err := proto.ReadLine(stream, &res); err != nil {
		return fmt.Errorf("точка не підтвердила: %w", err)
	}
	if !res.OK {
		return errors.New("точка: " + res.Error)
	}
	return nil
}

func (s *Server) dropJob(j *spoolJob, why string) {
	s.mu.Lock()
	delete(s.jobs, j.ID)
	st := s.statsLocked(portKey(j.AgentID, j.Alias))
	st.Failed++
	st.LastError = why
	name := s.agentNameLocked(j.AgentID)
	s.mu.Unlock()
	os.Remove(s.jobData(j.ID))
	os.Remove(s.jobMeta(j.ID))
	s.Log.Printf("друк %s/%s: завдання %s прибрано з черги: %s", name, j.Alias, j.ID, why)
}

// CancelJob removes a queued job (from the admin page).
func (s *Server) CancelJob(id string) error {
	s.mu.Lock()
	j := s.jobs[id]
	s.mu.Unlock()
	if j == nil {
		return errors.New("завдання немає (можливо, вже надруковано)")
	}
	s.dropJob(j, "скасовано адміністратором")
	return nil
}

// SetJobTTL changes how long undelivered jobs are kept.
func (s *Server) SetJobTTL(minutes int) error {
	if minutes < 1 || minutes > 7*24*60 {
		return errors.New("від 1 хвилини до 7 діб")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.JobTTLMinutes = minutes
	return s.save()
}

type JobView struct {
	ID        string    `json:"id"`
	Alias     string    `json:"alias"`
	Created   time.Time `json:"created"`
	Bytes     int64     `json:"bytes"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error"`
}

func (s *Server) jobViewsLocked(agentID string) []JobView {
	var out []JobView
	for _, j := range s.pendingLocked(agentID) {
		out = append(out, JobView{ID: j.ID, Alias: j.Alias, Created: j.Created, Bytes: j.Bytes, Attempts: j.Attempts, LastError: j.LastError})
	}
	return out
}
