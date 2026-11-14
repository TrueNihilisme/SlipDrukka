package common

import (
	"testing"
	"time"
)

// Failed sign-ins are counted per key: one user of a shared computer cannot
// lock the others out, and repeated blocks get longer.
func TestLimiterPerKey(t *testing.T) {
	l := NewLimiter(3, time.Minute, 40*time.Millisecond)
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("сеанс 7"); !ok {
			t.Fatalf("заблоковано після %d спроб", i)
		}
		l.Fail("сеанс 7")
	}
	if ok, wait := l.Allow("сеанс 7"); ok || wait <= 0 {
		t.Fatal("після трьох невдач вхід не заблоковано")
	}
	if ok, _ := l.Allow("сеанс 2"); !ok {
		t.Fatal("чужі невдачі заблокували іншого користувача")
	}
	time.Sleep(60 * time.Millisecond)
	if ok, _ := l.Allow("сеанс 7"); !ok {
		t.Fatal("блокування не минуло")
	}
	// The second block is twice as long as the first.
	for i := 0; i < 3; i++ {
		l.Fail("сеанс 7")
	}
	time.Sleep(60 * time.Millisecond)
	if ok, _ := l.Allow("сеанс 7"); ok {
		t.Fatal("повторне блокування не стало довшим")
	}
	// A successful sign-in forgets everything.
	l.Reset("сеанс 7")
	if ok, _ := l.Allow("сеанс 7"); !ok {
		t.Fatal("після успішного входу блокування лишилось")
	}
}
