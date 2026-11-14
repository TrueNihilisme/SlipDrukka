package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"slipdrukka/internal/common"
)

func TestLogin(t *testing.T) {
	s, err := New(t.TempDir(), common.NewLogRing("", 10), nopAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	CheckLogin = func(user, pw string) error {
		if user == "admin" && pw == "right" {
			return nil
		}
		return errors.New("невірне ім'я або пароль")
	}
	h := s.routes()
	try := func(remote, body string) int {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(body))
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := try("192.0.2.5:1000", `{"user":"admin","password":"right"}`); c != http.StatusForbidden {
		t.Fatalf("пароль Windows з мережі прийнято: %d", c)
	}
	if c := try("127.0.0.1:1000", `{"user":"admin","password":"right"}`); c != http.StatusOK {
		t.Fatalf("правильний вхід: %d", c)
	}
	if c := try("127.0.0.1:1000", `{"user":"","password":"x"}`); c != http.StatusUnauthorized {
		t.Fatalf("порожній аварійний пароль пустив: %d", c)
	}
	for i := 0; i < 4; i++ {
		try("127.0.0.1:1000", `{"user":"admin","password":"wrong"}`)
	}
	if c := try("127.0.0.1:1000", `{"user":"admin","password":"right"}`); c != http.StatusTooManyRequests {
		t.Fatalf("після 5 невдалих спроб мало блокувати: %d", c)
	}
}
