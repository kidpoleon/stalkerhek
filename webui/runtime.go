package webui

import (
	"context"
	"errors"
	"sync"
)

type runner struct {
	cancel context.CancelFunc
}

var (
	runMu   sync.RWMutex
	runners = map[int]*runner{}
)

// RegisterRunner registers a cancel func for a profile ID
func RegisterRunner(id int, cancel context.CancelFunc) {
	runMu.Lock()
	defer runMu.Unlock()
	runners[id] = &runner{cancel: cancel}
}

// StopRunner stops a running profile by ID
func StopRunner(id int) error {
	runMu.Lock()
	r := runners[id]
	if r == nil {
		runMu.Unlock()
		return errors.New("runner not found")
	}
	delete(runners, id)
	runMu.Unlock()
	r.cancel()
	return nil
}

// IsRunning checks if profile is registered
func IsRunning(id int) bool {
	runMu.RLock()
	defer runMu.RUnlock()
	_, ok := runners[id]
	return ok
}
