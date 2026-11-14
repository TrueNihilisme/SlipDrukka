package agent

import (
	"errors"

	"slipdrukka/internal/proto"
)

// adoptKey stores the point's own key from the server in place of the
// connection code (one-time codes, 0.18.0) and asks for an immediate
// reconnect. If saving fails the old code is kept: the server sends the same
// key again next time.
func (a *Agent) adoptKey(code proto.Code, key string) error {
	if key == "" || len(key) > 200 {
		return errors.New("сервер надіслав некоректний ключ")
	}
	next := code
	next.Key = key
	text := next.String()
	if _, err := proto.ParseCode(text); err != nil {
		return err
	}
	// The tunnel was built with the old key: close it before reconnecting.
	// Whether WireGuard got through recently is kept: the network is the same.
	a.dropWG()
	a.mu.Lock()
	old := a.cfg.Code
	a.cfg.Code = text
	if err := a.saveLocked(); err != nil {
		a.cfg.Code = old
		a.mu.Unlock()
		a.Log.Printf("ПОМИЛКА збереження власного ключа точки: %v — залишаю код підключення", err)
		return err
	}
	a.mu.Unlock()
	a.Log.Printf("сервер видав цій точці власний ключ; код підключення більше не потрібен")
	return errKicked
}
