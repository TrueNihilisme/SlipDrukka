package agent

import (
	"bufio"
	"net"
	"os"
	"time"

	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/winprint"
)

// handleJob serves one stream opened by the server: a print job or a
// command (see commands.go).
func (a *Agent) handleJob(st net.Conn) {
	a.busy.Add(1)
	defer a.busy.Add(-1)
	defer st.Close()
	br := bufio.NewReaderSize(st, proto.MaxFrame+4)
	st.SetReadDeadline(time.Now().Add(30 * time.Second))
	var hdr proto.JobHeader
	if err := proto.ReadLine(br, &hdr); err != nil {
		return
	}
	if hdr.Kind == proto.KindCmd {
		a.handleCmd(st, hdr)
		return
	}
	reply := func(res proto.JobResult) {
		st.SetWriteDeadline(time.Now().Add(30 * time.Second))
		proto.WriteLine(st, res)
		a.mu.Lock()
		if res.OK {
			a.jobs.OK++
		} else {
			a.jobs.Failed++
		}
		a.mu.Unlock()
	}

	a.mu.Lock()
	allowed, mode, alias := false, proto.ModeRaw, ""
	for _, p := range a.cfg.Shared {
		if p.Name == hdr.Printer {
			allowed, mode, alias = true, p.Mode, p.Alias
		}
	}
	a.mu.Unlock()
	if !allowed {
		a.Log.Printf("завдання %s: принтер «%s» не видано — відмова", hdr.ID, hdr.Printer)
		reply(proto.JobResult{Error: "принтер «" + hdr.Printer + "» не видано на точці"})
		return
	}

	buf := make([]byte, proto.MaxFrame)
	var total int64

	// The server re-sends a job when our confirmation got lost. Swallow the
	// data and confirm again instead of printing a second copy.
	a.mu.Lock()
	_, already := a.done[hdr.ID]
	a.mu.Unlock()
	if already {
		for {
			st.SetReadDeadline(time.Now().Add(5 * time.Minute))
			p, err := proto.ReadFrame(br, buf)
			if err != nil {
				return
			}
			if len(p) == 0 {
				break
			}
			total += int64(len(p))
		}
		a.Log.Printf("завдання %s вже надруковано раніше — повтор пропущено", hdr.ID)
		reply(proto.JobResult{OK: true, Bytes: total})
		return
	}

	target := hdr.Printer
	if mode == proto.ModeRaw {
		t, err := a.rawTarget(hdr.Printer, alias)
		if err != nil {
			// Read the rest so the server gets our answer rather than a broken
			// stream; it keeps the job and retries.
			for {
				st.SetReadDeadline(time.Now().Add(5 * time.Minute))
				p, err := proto.ReadFrame(br, buf)
				if err != nil || len(p) == 0 {
					break
				}
			}
			a.Log.Printf("завдання %s → «%s»: не надруковано — %v", hdr.ID, hdr.Printer, err)
			reply(proto.JobResult{Error: err.Error()})
			return
		}
		target = t
	}

	var job winprint.Job
	var err error
	if mode == proto.ModeXPS {
		job, err = a.openDocument(hdr.Printer, "SlipDrukka "+hdr.ID)
	} else {
		job, err = a.spool.Open(target, "SlipDrukka "+hdr.ID)
	}
	if err != nil {
		a.Log.Printf("завдання %s → «%s»: %v", hdr.ID, hdr.Printer, err)
		reply(proto.JobResult{Error: err.Error()})
		a.changed() // the printer may have been renamed or removed
		return
	}
	for {
		st.SetReadDeadline(time.Now().Add(5 * time.Minute))
		p, err := proto.ReadFrame(br, buf)
		if err != nil {
			job.Abort()
			a.Log.Printf("завдання %s → «%s»: обрив на %d байтах, скасовано: %v", hdr.ID, hdr.Printer, total, err)
			return // the server sees the stream fail and reports the job as failed
		}
		if len(p) == 0 {
			break
		}
		if _, err := job.Write(p); err != nil {
			job.Abort()
			a.Log.Printf("завдання %s → «%s»: %v", hdr.ID, hdr.Printer, err)
			reply(proto.JobResult{Error: err.Error(), Bytes: total})
			return
		}
		total += int64(len(p))
	}
	if err := job.Close(); err != nil {
		a.Log.Printf("завдання %s → «%s»: %v", hdr.ID, hdr.Printer, err)
		reply(proto.JobResult{Error: err.Error(), Bytes: total})
		return
	}
	a.markDone(hdr.ID, time.Duration(hdr.KeepMin)*time.Minute)
	how := "передано в чергу Windows"
	if mode == proto.ModeXPS {
		how = "документ надруковано драйвером цього ПК"
	}
	if target != hdr.Printer {
		how += " (службова черга «" + target + "»)"
	}
	a.Log.Printf("завдання %s → «%s»: %d байт, %s", hdr.ID, hdr.Printer, total, how)
	reply(proto.JobResult{OK: true, Bytes: total})
}

// rawSafeV4 lists v4 drivers measured to pass receipt data through unchanged
// (other v4 drivers make the point use its service printer, see fallback.go).
// Measured 2026-11-10 by sending RAW data through the spooler to a TCP
// capture: "Microsoft XPS Document Writer v4" passed it unchanged, while
// "Microsoft IPP Class Driver" and "Microsoft Print To PDF" (both v4) sent
// nothing at all although Windows reported the job as printed.
var rawSafeV4 = map[string]bool{"Microsoft XPS Document Writer v4": true}

// doneKeep: how long a printed job is remembered at least.
const doneKeep = 48 * time.Hour

// markDone remembers a printed job, on disk, so a re-sent job is recognised
// even after the agent restarts. It is kept for two days, or for as long as
// the server keeps jobs in its queue (serverKeep, from the job header) plus
// an hour when that is longer: the server may re-send a job until then.
func (a *Agent) markDone(id string, serverKeep time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	keep := doneKeep
	if serverKeep+time.Hour > keep {
		keep = serverKeep + time.Hour
	}
	now := time.Now()
	a.done[id] = now
	for k, t := range a.done {
		if now.Sub(t) > keep {
			delete(a.done, k)
		}
	}
	if err := common.SaveJSON(common.Join(a.dir, "done.json"), a.done); err != nil {
		a.Log.Printf("ПОМИЛКА збереження done.json: %v", err)
	}
}

// docJob collects a document-mode job in a temp file; Close hands the file
// to the spooler, which prints it through the local driver.
type docJob struct {
	a                *Agent
	f                *os.File
	printer, docName string
}

func (a *Agent) openDocument(printer, docName string) (winprint.Job, error) {
	dir := common.Join(a.dir, "tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "job-*.xps")
	if err != nil {
		return nil, err
	}
	return &docJob{a: a, f: f, printer: printer, docName: docName}, nil
}

func (j *docJob) Write(p []byte) (int, error) { return j.f.Write(p) }

func (j *docJob) Close() error {
	path := j.f.Name()
	defer os.Remove(path)
	if err := j.f.Close(); err != nil {
		return err
	}
	return j.a.spool.PrintXPS(j.printer, j.docName, path)
}

func (j *docJob) Abort() error {
	j.f.Close()
	return os.Remove(j.f.Name())
}
