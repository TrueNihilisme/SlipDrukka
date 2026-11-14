package server

import (
	"slipdrukka/internal/i18n"
)

// StateIn is State with every message translated into lang, for the panel
// of a viewer who reads that language. Names (shops, printers) stay as they
// are.
func (s *Server) StateIn(lang string) StateView {
	v := s.State()
	if lang != i18n.EN {
		return v
	}
	tr := func(x string) string { return i18n.Tr(lang, x) }
	v.Log = s.Log.LinesIn(lang)
	v.QueueError = tr(v.QueueError)
	v.Warning = tr(v.Warning)
	for i := range v.Refused {
		v.Refused[i].Reason = tr(v.Refused[i].Reason)
	}
	for i := range v.Bans {
		v.Bans[i].Reason = tr(v.Bans[i].Reason)
	}
	for i := range v.Audit {
		v.Audit[i].Who = tr(v.Audit[i].Who)
		v.Audit[i].Action = tr(v.Audit[i].Action)
		v.Audit[i].Detail = tr(v.Audit[i].Detail)
	}
	v.CrowdSec.Error = tr(v.CrowdSec.Error)
	for ai := range v.Agents {
		a := &v.Agents[ai]
		for i := range a.Printers {
			a.Printers[i].LastError = tr(a.Printers[i].LastError)
		}
		for i := range a.Jobs {
			a.Jobs[i].LastError = tr(a.Jobs[i].LastError)
		}
		for i := range a.Events {
			a.Events[i].Text = tr(a.Events[i].Text)
		}
	}
	return v
}
