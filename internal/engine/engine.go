package engine

import "errors"

const (
	StateUnavailable = "unavailable"
	StateStopped     = "stopped"
	StateStarting    = "starting"
	StateConnected   = "connected"
	StateStopping    = "stopping"
	StateFailed      = "failed"
)

var (
	ErrAlreadyRunning = errors.New("VPN engine is already running")
	ErrNotRunning     = errors.New("VPN engine is not running")
)

type Status struct {
	State     string `json:"state"`
	Available bool   `json:"available"`
	PID       int    `json:"pid,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	Error     string `json:"error,omitempty"`
}

type Controller interface {
	Status() Status
	Start(configuration []byte) error
	Stop() error
}

func Active(status Status) bool {
	switch status.State {
	case StateStarting, StateConnected, StateStopping:
		return true
	default:
		return false
	}
}

type Unavailable struct {
	reason string
}

func NewUnavailable(reason string) *Unavailable {
	return &Unavailable{reason: reason}
}

func (u *Unavailable) Status() Status {
	return Status{State: StateUnavailable, Error: u.reason}
}

func (u *Unavailable) Start([]byte) error {
	return errors.New(u.reason)
}

func (u *Unavailable) Stop() error {
	return ErrNotRunning
}
