package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"slipdrukka/internal/nettools"
)

// Network tools of the admin panel (0.17.0): where a shop is, and the route
// to it in both directions. Nothing here asks an outside service.

// TraceResult is one traceroute shown in the panel.
type TraceResult struct {
	From   string         `json:"from"` // "server" or "shop"
	Target string         `json:"target"`
	Hops   []nettools.Hop `json:"hops"`
}

// Trace runs a traceroute from the server to the shop's public address, or
// asks the shop to trace the route to the server.
func (s *Server) Trace(ctx context.Context, agentID, from string) (TraceResult, error) {
	s.mu.Lock()
	conn := s.online[agentID]
	name := s.agentNameLocked(agentID)
	s.mu.Unlock()
	if conn == nil {
		return TraceResult{}, fmt.Errorf("точка «%s» не на зв'язку", name)
	}
	switch from {
	case "server":
		target := conn.seenAs
		if ip := net.ParseIP(target); ip == nil || ip.IsLoopback() {
			return TraceResult{}, fmt.Errorf("невідома зовнішня адреса точки «%s»", name)
		}
		hops, err := nettools.Trace(ctx, target)
		if err != nil {
			return TraceResult{}, err
		}
		s.Log.Printf("трасування до точки «%s» (%s): %d переходів", name, target, len(hops))
		return TraceResult{From: from, Target: target, Hops: hops}, nil
	case "shop":
		data, err := s.Command(agentID, "traceroute", nil)
		if err != nil {
			return TraceResult{}, err
		}
		var r TraceResult
		if err := json.Unmarshal(data, &r); err != nil {
			return TraceResult{}, fmt.Errorf("точка повернула незрозумілу відповідь")
		}
		r.From = from
		s.Log.Printf("трасування від точки «%s» до сервера: %d переходів", name, len(r.Hops))
		return r, nil
	}
	return TraceResult{}, fmt.Errorf("невідомий напрям трасування")
}

// ServerNet is the server's own side for the panel.
type ServerNet struct {
	Ifaces []nettools.Iface `json:"ifaces"`
	Public string           `json:"public"` // the public address set in Settings
}

func (s *Server) NetInfo() ServerNet {
	s.mu.Lock()
	pub := s.cfg.PublicAddr
	s.mu.Unlock()
	return ServerNet{Ifaces: nettools.LocalAddrs(), Public: pub}
}

var traceTimeout = 90 * time.Second
