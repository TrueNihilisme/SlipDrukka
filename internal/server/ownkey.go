package server

import (
	"slipdrukka/internal/common"
	"slipdrukka/internal/proto"
	"slipdrukka/internal/wgtun"
)

// One-time connection codes (0.18.0). A code is shown in chats, written on
// USB sticks and in verbose installer logs; it should open the door once.
// When a point that can take its own key connects with its code, the server
// issues a new long-term key (Welcome.NewKey) and keeps it as pending. The
// code still works until the point connects with the new key: a point that
// lost power before saving it simply gets the same key again. The first
// connection with the new key makes it the point's key and the code stops
// working.

// ownKeyLocked returns the key to send to the point ("" = none) and whether
// the config changed. s.mu held.
func (s *Server) ownKeyLocked(a *AgentConfig, hello proto.Hello, usedPending bool) (string, bool) {
	if usedPending {
		a.KeyHash = common.SHA256Hex(a.PendingKey)
		a.PendingKey = ""
		a.OwnKey, a.CodeUsed = true, true
		s.Log.Printf("точка «%s» отримала власний ключ, її код підключення більше не діє", a.Name)
		return "", true
	}
	if a.OwnKey || !hello.HasCap(proto.CapOwnKey) {
		return "", false
	}
	if a.PendingKey != "" {
		return a.PendingKey, false // not saved by the point last time: the same key again
	}
	a.PendingKey = common.RandomToken(24)
	// Let the point's next WireGuard handshake, made with the new key, in.
	if err := s.updatePeersLocked(); err != nil {
		s.Log.Printf("ПОМИЛКА WireGuard: %v", err)
	}
	s.Log.Printf("точці «%s» видано власний ключ замість коду підключення", a.Name)
	return a.PendingKey, true
}

// pendingWGPub: the WireGuard key a point will have with its pending key.
func pendingWGPub(a *AgentConfig) string {
	if a.PendingKey == "" {
		return ""
	}
	return wgtun.PointPublic(a.PendingKey).String()
}
