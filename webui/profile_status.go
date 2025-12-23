package webui

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"github.com/CrazeeGhost/stalkerhek/stalker"
)

// ProfileStatus represents per-profile status in dashboard
type ProfileStatus struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Phase    string `json:"phase"`
	Message  string `json:"message"`
	Channels int    `json:"channels"`
	HLS      string `json:"hls"`
	Proxy    string `json:"proxy"`
	Running  bool   `json:"running"`
}

var (
	psMu   sync.RWMutex
	pstate = map[int]ProfileStatus{}
)

// GetProfileStatus returns the current status for a profile ID
func GetProfileStatus(id int) ProfileStatus {
	psMu.RLock()
	defer psMu.RUnlock()
	s, ok := pstate[id]
	if !ok {
		return ProfileStatus{ID: id, Name: "", Phase: "idle", Message: "Unknown", Running: false}
	}
	return s
}

func SetProfileValidating(id int, name, msg string) {
	psMu.Lock()
	s := pstate[id]
	s.ID, s.Name, s.Phase, s.Message = id, name, "validating", msg
	pstate[id] = s
	psMu.Unlock()
}

func SetProfileError(id int, name, msg string) {
	psMu.Lock()
	s := pstate[id]
	s.ID, s.Name, s.Phase, s.Message, s.Running = id, name, "error", msg, false
	pstate[id] = s
	psMu.Unlock()
}

func SetProfileSuccess(id int, name string, channels int, hls, proxy string, running bool) {
	psMu.Lock()
	pstate[id] = ProfileStatus{ID: id, Name: name, Phase: "success", Message: "Verified", Channels: channels, HLS: hls, Proxy: proxy, Running: running}
	psMu.Unlock()
}

func SetProfileStopped(id int) {
	psMu.Lock()
	s := pstate[id]
	s.Phase, s.Message, s.Running = "idle", "Stopped", false
	pstate[id] = s
	psMu.Unlock()
}

// RegisterProfileStatusHandlers mounts /api/profile_status for polling
func RegisterProfileStatusHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/api/profile_status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		psMu.RLock()
		arr := make([]ProfileStatus, 0, len(pstate))
		for _, v := range pstate {
			arr = append(arr, v)
		}
		psMu.RUnlock()
		sort.Slice(arr, func(i, j int) bool { return arr[i].ID < arr[j].ID })
		_ = json.NewEncoder(w).Encode(arr)
	})

	// stop endpoint: POST /profiles/{id}/stop
	mux.HandleFunc("/profiles/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		idStr := r.FormValue("id")
		if idStr == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		id := atoiSafe(idStr)
		_ = StopRunner(id)
		SetProfileStopped(id)
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	})

	// delete endpoint: POST /profiles/{id}/delete
	mux.HandleFunc("/profiles/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := atoiSafe(r.FormValue("id"))
		DeleteProfile(id)
		_ = SaveProfiles()
		SetProfileStopped(id)
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	})

	// verify-only endpoint: POST /profiles/{id}/verify
	mux.HandleFunc("/profiles/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := atoiSafe(r.FormValue("id"))
		p, ok := GetProfile(id)
		if !ok {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
		SetProfileValidating(p.ID, p.Name, "Verifying...")
		host := r.Host
		go func(p Profile, host string) {
			// Minimal verification without starting services
			base := *DefaultPortal()
			cfg := &stalker.Config{Portal: &base}
			cfg.Portal.Location = p.PortalURL
			cfg.Portal.MAC = p.MAC
			if err := cfg.Portal.Start(); err != nil {
				SetProfileError(p.ID, p.Name, err.Error())
				return
			}
			chs, err := cfg.Portal.RetrieveChannels()
			if err != nil {
				SetProfileError(p.ID, p.Name, err.Error())
				return
			}
			SetProfileSuccess(p.ID, p.Name, len(chs), linkForHost(host, p.HlsPort), linkForHost(host, p.ProxyPort), false)
		}(p, host)
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	})
}

// helper: safe atoi
func atoiSafe(s string) int { n := 0; for _, c := range s { if c < '0' || c > '9' { break }; n = n*10 + int(c-'0') }; return n }

func itoa(n int) string { if n == 0 { return "0" }; s := ""; for n > 0 { s = string('0'+(n%10)) + s; n/=10 }; return s }

// linkForHost composes an http://host:port/ link given a raw host header
func linkForHost(raw string, port int) string {
	host := raw
	if i := strings.Index(host, ":"); i > -1 {
		host = host[:i]
	}
	return "http://" + host + ":" + itoa(port) + "/"
}

// DefaultPortal returns a baseline Portal config used for verification
func DefaultPortal() *stalker.Portal {
return &stalker.Portal{
Model:        "MAG254",
SerialNumber: "0000000000000",
DeviceID:     strings.Repeat("f", 64),
DeviceID2:    strings.Repeat("f", 64),
Signature:    strings.Repeat("f", 64),
TimeZone:     "UTC",
DeviceIdAuth: true,
WatchDogTime: 5,
}
}
