package session

import (
	"errors"

	"ramus/internal/git/status"
	"ramus/internal/git/write"
)

// TurnOnMonitor sets the config a offers, through git.
func (s *Session) TurnOnMonitor(a status.MonitorAdvice) (*write.Job, error) {
	if len(a.Cmds) == 0 {
		return nil, errors.New("session: nothing to turn on")
	}
	j, err := s.Submit(write.Op{Name: "Turn on faster status", Cmd: a.Cmds[0], More: a.Cmds[1:]})
	if err == nil {
		s.DismissMonitor()
	}
	return j, err
}

// DismissMonitor stops offering the monitor in this session.
func (s *Session) DismissMonitor() {
	s.monitorAnswered.Store(true)
	s.State.Monitor = nil
}
