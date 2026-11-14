package server

// Central management of points: the inventory each point pushes, the events
// it reports, and commands sent to it over its tunnel.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"slipdrukka/internal/proto"
)

const maxServerEvents = 50

// handlePush reads one stream opened by the point.
func (s *Server) handlePush(conn *agentConn, st net.Conn) {
	defer st.Close()
	st.SetReadDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(st)
	var hdr proto.JobHeader
	if proto.ReadLine(br, &hdr) != nil || hdr.Kind != proto.KindInventory {
		return
	}
	var inv proto.Inventory
	if err := proto.ReadLine(br, &inv); err != nil {
		return
	}
	s.applyInventory(conn, &inv)
}

// applyInventory stores a report and adopts the point's shared printers
// (names may have changed: Windows renamed a printer, admin renamed it).
func (s *Server) applyInventory(conn *agentConn, inv *proto.Inventory) {
	printers := cleanPrinters(inv.Shared)
	s.mu.Lock()
	conn.inv, conn.invAt = inv, time.Now()
	conn.printers = printers
	if s.online[conn.id] == conn {
		for _, p := range printers {
			s.ensurePortLocked(conn.id, p.Alias)
		}
	}
	s.mu.Unlock()
	s.noteEvents(conn.id, inv)
	s.wake(conn.id) // a renamed printer may unblock queued jobs
}

// noteEvents keeps the point's events (they arrive with every report) and
// writes new warnings/errors to the server log once.
func (s *Server) noteEvents(agentID string, inv *proto.Inventory) {
	if inv == nil {
		return
	}
	s.mu.Lock()
	name := s.agentNameLocked(agentID)
	known := s.events[agentID]
	have := map[string]bool{}
	for _, e := range known {
		have[e.Time.Format(time.RFC3339Nano)+e.Text] = true
	}
	var fresh []proto.Event
	for _, e := range inv.Events {
		// Events come from the point: treat them as untrusted input.
		if e.Level != "info" && e.Level != "warn" && e.Level != "error" {
			continue
		}
		e.Kind = proto.SanitizeName(e.Kind)
		if r := []rune(e.Text); len(r) > 500 {
			e.Text = string(r[:500]) + "…"
		}
		if r := []rune(e.Printer); len(r) > 100 {
			e.Printer = string(r[:100])
		}
		if !have[e.Time.Format(time.RFC3339Nano)+e.Text] {
			fresh = append(fresh, e)
			known = append(known, e)
		}
	}
	if len(known) > maxServerEvents {
		known = known[len(known)-maxServerEvents:]
	}
	s.events[agentID] = known
	s.mu.Unlock()
	for _, e := range fresh {
		if e.Level != "info" {
			s.Log.Printf("точка «%s»: %s", name, e.Text)
		}
	}
}

// FreePort releases the local port of a printer the point no longer offers
// (ports are otherwise kept forever so that Windows queues stay valid).
// Refused while a Windows queue on this server still uses the port: the
// number would later go to another printer and that queue would print there.
func (s *Server) FreePort(agentID, alias string) error {
	s.qMu.Lock()
	s.qCache = nil // need a fresh answer, not the cached one
	s.qMu.Unlock()
	queues, err := s.queues()
	if err != nil {
		return fmt.Errorf("не вдалося перевірити принтери сервера: %w", err)
	}
	key := portKey(agentID, alias)
	s.mu.Lock()
	port, ok := s.cfg.Ports[key]
	if !ok {
		s.mu.Unlock()
		return errors.New("такого порту вже немає")
	}
	if q := queues[PortName(port)]; len(q) > 0 {
		s.mu.Unlock()
		names := make([]string, len(q))
		for i, x := range q {
			names[i] = "«" + x.Name + "»"
		}
		return fmt.Errorf("порт 127.0.0.1:%d ще використовує принтер сервера %s — спершу видаліть його у Windows сервера", port, strings.Join(names, ", "))
	}
	conn := s.online[agentID]
	if conn == nil {
		s.mu.Unlock()
		return errors.New("точка не на зв'язку — не видно, чи принтер ще видано")
	}
	for _, p := range conn.printers {
		if p.Alias == alias {
			s.mu.Unlock()
			return errors.New("принтер ще видано — спершу «Прибрати»")
		}
	}
	if l := s.listeners[key]; l != nil {
		l.Close()
		delete(s.listeners, key)
	}
	delete(s.cfg.Ports, key)
	delete(s.stats, key)
	var drop []*spoolJob
	for _, j := range s.jobs {
		if j.AgentID == agentID && j.Alias == alias {
			drop = append(drop, j)
		}
	}
	name := s.agentNameLocked(agentID)
	err = s.save()
	s.mu.Unlock()
	for _, j := range drop {
		s.dropJob(j, "порт звільнено")
	}
	s.Log.Printf("точка «%s»: порт 127.0.0.1:%d («%s») звільнено", name, port, alias)
	return err
}

// allowedCmds is what the admin page may ask a point to do.
var allowedCmds = map[string]bool{
	"set_shared": true, "add_net_printer": true, "add_port_printer": true,
	"rename_printer": true, "test_port": true, "drivers": true,
	"set_driver": true, "set_printer_ip": true,
}

// Command runs a command on a connected point and returns its data.
func (s *Server) Command(agentID, cmd string, args json.RawMessage) (json.RawMessage, error) {
	if !allowedCmds[cmd] {
		return nil, fmt.Errorf("невідома команда «%s»", cmd)
	}
	s.mu.Lock()
	conn := s.online[agentID]
	name := s.agentNameLocked(agentID)
	s.mu.Unlock()
	if conn == nil {
		return nil, fmt.Errorf("точка «%s» не на зв'язку", name)
	}
	st, err := conn.sess.Open()
	if err != nil {
		return nil, fmt.Errorf("канал до точки: %w", err)
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(90 * time.Second)) // creating a printer runs PowerShell on the point
	if err := proto.WriteLine(st, proto.JobHeader{Kind: proto.KindCmd, Cmd: cmd, Args: args}); err != nil {
		return nil, err
	}
	var res proto.CmdResult
	if err := proto.ReadLine(st, &res); err != nil {
		return nil, fmt.Errorf("точка не відповіла: %w", err)
	}
	if !res.OK {
		return nil, errors.New(res.Error)
	}
	if cmd != "drivers" {
		s.Log.Printf("точка «%s»: виконано «%s»", name, cmd)
	}
	return res.Data, nil
}

// RenameAgent changes a point's display name (queues and ports stay).
func (s *Server) RenameAgent(id, name string) error {
	name = proto.SanitizeName(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.findAgentLocked(id)
	if a == nil {
		return errors.New("точки немає")
	}
	for _, x := range s.cfg.Agents {
		if x.ID != id && strings.EqualFold(x.Name, name) {
			return fmt.Errorf("точка «%s» вже є", name)
		}
	}
	old := a.Name
	a.Name = name
	s.Log.Printf("точку «%s» перейменовано на «%s»", old, name)
	return s.save()
}

// RenameQueue renames a Windows printer on this server.
func (s *Server) RenameQueue(old, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" || strings.ContainsAny(newName, `\,`) || len([]rune(newName)) > 100 {
		return errors.New("назва принтера: до 100 символів, без «\\» і коми")
	}
	if err := s.admin.RenameQueue(old, newName); err != nil {
		return err
	}
	s.qMu.Lock()
	s.qCache = nil
	s.qMu.Unlock()
	s.Log.Printf("принтер сервера «%s» перейменовано на «%s»", old, newName)
	return nil
}
