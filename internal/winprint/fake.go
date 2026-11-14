package winprint

import (
	"bytes"
	"fmt"
	"os"
	"sync"
)

// Fake is an in-memory spooler for tests: every finished job is kept.
type Fake struct {
	mu       sync.Mutex
	Printers []PrinterInfo
	Done     map[string][][]byte // printer -> finished jobs
	Versions map[string]int      // driver -> version
}

func (f *Fake) DriverVersions() (map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for k, v := range f.Versions {
		out[k] = v
	}
	return out, nil
}

// Set replaces the printer list (tests simulate Windows changes with it).
func (f *Fake) Set(list []PrinterInfo) {
	f.mu.Lock()
	f.Printers = list
	f.mu.Unlock()
}

func (f *Fake) List() ([]PrinterInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PrinterInfo(nil), f.Printers...), nil
}

func (f *Fake) Open(printer, _ string) (Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.Printers {
		if p.Name == printer {
			return &fakeJob{f: f, printer: printer}, nil
		}
	}
	return nil, fmt.Errorf("принтера %q немає", printer)
}

// Jobs returns a copy of the finished jobs for printer.
func (f *Fake) Jobs(printer string) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.Done[printer]...)
}

type fakeJob struct {
	f       *Fake
	printer string
	buf     bytes.Buffer
}

func (j *fakeJob) Write(p []byte) (int, error) { return j.buf.Write(p) }

func (j *fakeJob) Close() error {
	j.f.mu.Lock()
	defer j.f.mu.Unlock()
	if j.f.Done == nil {
		j.f.Done = map[string][][]byte{}
	}
	j.f.Done[j.printer] = append(j.f.Done[j.printer], j.buf.Bytes())
	return nil
}

func (j *fakeJob) Abort() error { return nil }

// PrintXPS records the document bytes like a finished job.
func (f *Fake) PrintXPS(printer, _ string, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Done == nil {
		f.Done = map[string][][]byte{}
	}
	f.Done[printer] = append(f.Done[printer], b)
	return nil
}
