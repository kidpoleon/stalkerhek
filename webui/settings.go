package webui

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/kidpoleon/stalkerhek/hls"
	"github.com/kidpoleon/stalkerhek/proxy"
)

// DefaultRuntimeSettings are production-oriented defaults for IPTV HLS proxying.
// Tuned for stability over minimum latency (see README Advanced settings).
var DefaultRuntimeSettings = RuntimeSettings{
	PlaylistDelaySegments:        5,
	ResponseHeaderTimeoutSeconds: 35,
	MaxIdleConnsPerHost:          128,
	HLSChannelLinkTTLSeconds:     180,
	MediaChannelLinkTTLSeconds:   45,
}

type RuntimeSettings struct {
	PlaylistDelaySegments        int `json:"playlist_delay_segments"`
	ResponseHeaderTimeoutSeconds int `json:"response_header_timeout_seconds"`
	MaxIdleConnsPerHost          int `json:"max_idle_conns_per_host"`
	HLSChannelLinkTTLSeconds     int `json:"hls_channel_link_ttl_seconds"`
	MediaChannelLinkTTLSeconds   int `json:"media_channel_link_ttl_seconds"`
}

var (
	settingsMu      sync.RWMutex
	runtimeSettings = DefaultRuntimeSettings
)

func init() {
	applyRuntimeSettings(DefaultRuntimeSettings)
}

func GetRuntimeSettings() RuntimeSettings {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return runtimeSettings
}

func applyRuntimeSettings(s RuntimeSettings) {
	if s.PlaylistDelaySegments < 0 {
		s.PlaylistDelaySegments = 0
	}
	if s.PlaylistDelaySegments > 40 {
		s.PlaylistDelaySegments = 40
	}
	if s.ResponseHeaderTimeoutSeconds < 1 {
		s.ResponseHeaderTimeoutSeconds = 1
	}
	if s.ResponseHeaderTimeoutSeconds > 120 {
		s.ResponseHeaderTimeoutSeconds = 120
	}
	if s.MaxIdleConnsPerHost < 2 {
		s.MaxIdleConnsPerHost = 2
	}
	if s.MaxIdleConnsPerHost > 256 {
		s.MaxIdleConnsPerHost = 256
	}
	if s.HLSChannelLinkTTLSeconds < 30 {
		s.HLSChannelLinkTTLSeconds = 30
	}
	if s.HLSChannelLinkTTLSeconds > 600 {
		s.HLSChannelLinkTTLSeconds = 600
	}
	if s.MediaChannelLinkTTLSeconds < 5 {
		s.MediaChannelLinkTTLSeconds = 5
	}
	if s.MediaChannelLinkTTLSeconds > 120 {
		s.MediaChannelLinkTTLSeconds = 120
	}

	settingsMu.Lock()
	runtimeSettings = s
	settingsMu.Unlock()

	hls.SetPlaylistDelaySegments(s.PlaylistDelaySegments)
	hls.UpdateResponseHeaderTimeout(time.Duration(s.ResponseHeaderTimeoutSeconds) * time.Second)
	hls.UpdateMaxIdleConnsPerHost(s.MaxIdleConnsPerHost)
	hls.SetChannelLinkTTL(
		time.Duration(s.HLSChannelLinkTTLSeconds)*time.Second,
		time.Duration(s.MediaChannelLinkTTLSeconds)*time.Second,
	)

	proxy.UpdateResponseHeaderTimeout(time.Duration(s.ResponseHeaderTimeoutSeconds) * time.Second)
	proxy.UpdateMaxIdleConnsPerHost(s.MaxIdleConnsPerHost)

	LogInfo("SETTINGS", "applied playlist_delay=%d header_timeout=%ds idle_conns=%d hls_link_ttl=%ds",
		s.PlaylistDelaySegments, s.ResponseHeaderTimeoutSeconds, s.MaxIdleConnsPerHost, s.HLSChannelLinkTTLSeconds)
}

func RegisterSettingsHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(GetRuntimeSettings())
			return
		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			cur := GetRuntimeSettings()
			if v := r.FormValue("playlist_delay_segments"); v != "" {
				cur.PlaylistDelaySegments = atoiSafe(v)
			}
			if v := r.FormValue("response_header_timeout_seconds"); v != "" {
				cur.ResponseHeaderTimeoutSeconds = atoiSafe(v)
			}
			if v := r.FormValue("max_idle_conns_per_host"); v != "" {
				cur.MaxIdleConnsPerHost = atoiSafe(v)
			}
			if v := r.FormValue("hls_channel_link_ttl_seconds"); v != "" {
				cur.HLSChannelLinkTTLSeconds = atoiSafe(v)
			}
			if v := r.FormValue("media_channel_link_ttl_seconds"); v != "" {
				cur.MediaChannelLinkTTLSeconds = atoiSafe(v)
			}
			applyRuntimeSettings(cur)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "settings": GetRuntimeSettings()})
			return
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
	})
}
