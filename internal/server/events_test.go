package server

import (
	"strings"
	"testing"
	"time"

	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
)

// Events come from the point and are shown in the server panel: an unknown
// level (which would land in a class="" attribute) must be dropped and long
// texts cut.
func TestEventsFromPointSanitised(t *testing.T) {
	s, err := New(t.TempDir(), common.NewLogRing("", 10), nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.noteEvents("a1", &proto.Inventory{Events: []proto.Event{
		{Time: now, Level: `x" onmouseover="alert(1)`, Text: "bad"},
		{Time: now.Add(time.Second), Level: "warn", Kind: "<b>", Text: strings.Repeat("я", 900)},
	}})
	got := s.events["a1"]
	if len(got) != 1 {
		t.Fatalf("очікувалась 1 подія, є %d", len(got))
	}
	if got[0].Level != "warn" || len([]rune(got[0].Text)) > 501 || strings.ContainsAny(got[0].Kind, "<>") {
		t.Fatalf("подію не очищено: %+v", got[0].Level)
	}
}
