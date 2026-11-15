package server

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"slipdrukka/internal/common"
	"slipdrukka/internal/winprint"
)

type nopAdmin struct{}

func (nopAdmin) Drivers() ([]string, error)                    { return nil, nil }
func (nopAdmin) QueuesByPort() (map[string][]Queue, error)     { return nil, nil }
func (nopAdmin) CreateQueue(string, string, string, int) error { return nil }
func (nopAdmin) RenameQueue(string, string) error              { return nil }
func (nopAdmin) QueuePaper(string) (winprint.Paper, error) {
	return winprint.Paper{}, errors.New("no paper")
}
func (nopAdmin) SetQueuePaper(string, int, int) (winprint.Paper, error) {
	return winprint.Paper{}, errors.New("no paper")
}

// Jobs older than the TTL are dropped instead of printed late; jobs on disk
// are picked up again after a restart.
func TestSpoolTTLAndRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, common.NewLogRing("", 10), nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range []*spoolJob{
		{ID: "old", Seq: 1, AgentID: "a1", Alias: "p", Created: time.Now().Add(-2 * time.Hour)},
		{ID: "new", Seq: 2, AgentID: "a1", Alias: "p", Created: time.Now()},
	} {
		os.WriteFile(s.jobData(j.ID), []byte("x"), 0o600)
		common.SaveJSON(s.jobMeta(j.ID), j)
	}
	s2, err := New(dir, common.NewLogRing("", 10), nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.jobs) != 2 {
		t.Fatalf("після перезапуску в черзі %d завдань, мало бути 2", len(s2.jobs))
	}
	s2.deliverPending("a1") // point offline: only expiry can happen
	if _, ok := s2.jobs["old"]; ok {
		t.Fatal("прострочене завдання не прибрано")
	}
	if _, ok := s2.jobs["new"]; !ok {
		t.Fatal("свіже завдання зникло")
	}
	if _, err := os.Stat(s2.jobData("old")); !os.IsNotExist(err) {
		t.Fatal("файл простроченого завдання лишився")
	}
}

// Queue files damaged by a power cut (a cut-off description, data without a
// description, a description without data) do not stop the server from
// starting and do not take the good jobs with them.
func TestSpoolSurvivesDamagedFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, common.NewLogRing("", 10), nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	good := &spoolJob{ID: "good", Seq: 7, AgentID: "a1", Alias: "p", Created: time.Now(), Bytes: 1}
	os.WriteFile(s.jobData("good"), []byte("x"), 0o600)
	common.SaveJSON(s.jobMeta("good"), good)
	os.WriteFile(s.jobMeta("cut"), []byte(`{"id":"cut","seq":8,"agent":"a1","al`), 0o600) // cut-off description
	os.WriteFile(s.jobData("cut"), []byte("half a receipt"), 0o600)
	os.WriteFile(s.jobData("orphan"), []byte("data only"), 0o600) // data, no description
	common.SaveJSON(s.jobMeta("nodata"), &spoolJob{ID: "nodata", Seq: 9, AgentID: "a1", Alias: "p", Created: time.Now()})
	os.WriteFile(s.jobMeta("empty"), nil, 0o600) // empty description

	s2, err := New(dir, common.NewLogRing("", 10), nopAdmin{})
	if err != nil {
		t.Fatalf("сервер не стартував з пошкодженою чергою: %v", err)
	}
	if len(s2.jobs) != 1 || s2.jobs["good"] == nil {
		t.Fatalf("у черзі мало лишитися одне ціле завдання, є %d", len(s2.jobs))
	}
	if s2.seq < 7 {
		t.Fatalf("лічильник порядку %d, новий чек отримав би номер, менший за вже наявний", s2.seq)
	}
	for _, f := range []string{s2.jobData("cut"), s2.jobData("orphan"), s2.jobMeta("nodata")} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("сміття лишилось на диску: %s", filepath.Base(f))
		}
	}
	left, _ := filepath.Glob(filepath.Join(s2.spoolDir(), "*"))
	var names []string
	for _, f := range left {
		names = append(names, filepath.Base(f))
	}
	t.Logf("у теці черги після старту: %v", names)
}
