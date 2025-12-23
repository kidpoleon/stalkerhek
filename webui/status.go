package webui

import (
	"encoding/json"
	"net/http"
	"sync"
)

// Status provides verification feedback for the WebUI
type Status struct {
	Phase    string `json:"phase"`
	Message  string `json:"message"`
	Channels int    `json:"channels"`
}

var (
	stMu  sync.RWMutex
	curSt = Status{Phase: "idle", Message: "Waiting for configuration"}
)

// SetValidating marks the status as validating
func SetValidating(msg string) {
	stMu.Lock()
	curSt = Status{Phase: "validating", Message: msg}
	stMu.Unlock()
}

// SetError sets an error status
func SetError(msg string) {
	stMu.Lock()
	curSt = Status{Phase: "error", Message: msg}
	stMu.Unlock()
}

// SetSuccess marks credentials as verified and services running
func SetSuccess(ch int) {
	stMu.Lock()
	curSt = Status{Phase: "success", Message: "Credentials accepted. Services are running.", Channels: ch}
	stMu.Unlock()
}

// RegisterStatusHandlers mounts /status (JSON) and /dashboard (HTML) routes
func RegisterStatusHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		stMu.RLock()
		defer stMu.RUnlock()
		_ = json.NewEncoder(w).Encode(curSt)
	})

}
