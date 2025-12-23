package webui

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"runtime"
	"sync"
	"time"
)

// Health represents overall service health
type Health struct {
	Status      string            `json:"status"`
	Uptime      string            `json:"uptime"`
	Version     string            `json:"version"`
	Profiles    int               `json:"profiles"`
	Running     int               `json:"running"`
	Errors      int               `json:"errors"`
	WebUI       bool              `json:"webui"`
	Checks      map[string]string `json:"checks"`
	Timestamp   time.Time         `json:"timestamp"`
}

// Metrics represents runtime metrics
type Metrics struct {
	UptimeSeconds   int64   `json:"uptime_seconds"`
	GoRoutines      int     `json:"goroutines"`
	MemAllocMB      float64 `json:"mem_alloc_mb"`
	MemTotalMB      float64 `json:"mem_total_mb"`
	MemSysMB        float64 `json:"mem_sys_mb"`
	GCPausesTotal   uint64  `json:"gc_pauses_total"`
	NumGC           uint32  `json:"num_gc"`
	ProfilesTotal   int     `json:"profiles_total"`
	ProfilesRunning int     `json:"profiles_running"`
	ProfilesError   int     `json:"profiles_error"`
	RequestsTotal   uint64  `json:"requests_total"`
	ErrorsTotal     uint64  `json:"errors_total"`
	Timestamp       time.Time `json:"timestamp"`
}

var (
	startTime   = time.Now()
	healthMu    sync.RWMutex
	reqCounter  uint64
	errCounter  uint64
)

// RegisterHealthHandlers mounts /health, /metrics, and /info endpoints
func RegisterHealthHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		healthMu.RLock()
		defer healthMu.RUnlock()

		profiles := ListProfiles()
		running, errors := 0, 0
		for _, p := range profiles {
			st := GetProfileStatus(p.ID)
			if st.Running {
				running++
			}
			if st.Phase == "error" {
				errors++
			}
		}

		status := "healthy"
		if len(profiles) == 0 {
			status = "no_profiles"
		} else if errors > 0 {
			status = "degraded"
		}

		checks := map[string]string{
			"webui":    "ok",
			"storage":  "ok",
			"profiles": fmt.Sprintf("%d total, %d running, %d errors", len(profiles), running, errors),
		}

		h := Health{
			Status:    status,
			Uptime:    time.Since(startTime).Round(time.Second).String(),
			Version:   "dev",
			Profiles:  len(profiles),
			Running:   running,
			Errors:    errors,
			WebUI:     true,
			Checks:    checks,
			Timestamp: time.Now().UTC(),
		}

		w.Header().Set("Content-Type", "application/json")
		if status == "healthy" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(h)
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		healthMu.RLock()
		defer healthMu.RUnlock()

		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		profiles := ListProfiles()
		running, errors := 0, 0
		for _, p := range profiles {
			st := GetProfileStatus(p.ID)
			if st.Running {
				running++
			}
			if st.Phase == "error" {
				errors++
			}
		}

		met := Metrics{
			UptimeSeconds:   int64(time.Since(startTime).Seconds()),
			GoRoutines:      runtime.NumGoroutine(),
			MemAllocMB:      float64(m.Alloc) / 1024 / 1024,
			MemTotalMB:      float64(m.TotalAlloc) / 1024 / 1024,
			MemSysMB:        float64(m.Sys) / 1024 / 1024,
			GCPausesTotal:   m.PauseTotalNs,
			NumGC:           m.NumGC,
			ProfilesTotal:   len(profiles),
			ProfilesRunning: running,
			ProfilesError:   errors,
			RequestsTotal:   reqCounter,
			ErrorsTotal:     errCounter,
			Timestamp:       time.Now().UTC(),
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(met)
	})

	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		profiles := ListProfiles()
		data := struct {
			Host      string
			Uptime    string
			Profiles  []Profile
			GoVersion string
			NumGo     int
		}{
			Host:      r.Host,
			Uptime:    time.Since(startTime).Round(time.Second).String(),
			Profiles:  profiles,
			GoVersion: runtime.Version(),
			NumGo:     runtime.NumGoroutine(),
		}

		const tpl = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Stalkerhek Info</title>
  <style>
    :root{--bg:#0a0f0a;--panel:#0d1410;--panel2:#111815;--border:#1f2e23;--text:#e0e6e0;--muted:#9aaa9a;--brand:#2d7a4e;--brand-hover:#3a8f5e;--ok:#3fb970;--warn:#d4a94a;--bad:#e85d4d}
    *{box-sizing:border-box}
    body{margin:0;font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Helvetica,Arial,sans-serif;background:linear-gradient(180deg, #0d1410 0%, #0a0f0a 100%);color:var(--text);min-height:100vh}
    .wrap{max-width:900px;margin:0 auto;padding:16px 12px}
    .card{background:linear-gradient(180deg, rgba(17,24,21,.96), rgba(13,20,16,.94));border:1px solid var(--border);border-radius:16px;padding:20px;box-shadow:0 12px 32px rgba(0,0,0,.4);margin-bottom:16px}
    h1{margin:0 0 8px 0;font-size:24px;color:var(--text)}
    h2{margin:0 0 12px 0;font-size:18px;color:var(--text)}
    .sub{color:var(--muted);font-size:14px;margin:4px 0}
    .grid{display:grid;grid-template-columns:repeat(auto-fit, minmax(200px, 1fr));gap:12px}
    .stat{background:rgba(31,46,35,.3);border:1px solid var(--border);border-radius:10px;padding:12px}
    .stat-label{font-size:12px;color:var(--muted);text-transform:uppercase;letter-spacing:.5px}
    .stat-value{font-size:20px;font-weight:700;color:var(--text);margin-top:4px}
    .ok{color:var(--ok)}
    .err{color:var(--bad)}
    table{width:100%;border-collapse:collapse;margin-top:12px}
    th,td{text-align:left;padding:8px 12px;border-bottom:1px solid var(--border)}
    th{font-weight:600;color:var(--text)}
    td{font-size:14px;color:var(--muted)}
    .badge{display:inline-block;padding:4px 8px;border-radius:999px;font-size:12px;font-weight:600}
    .badge.ok{background:rgba(63,185,112,.2);color:#bfffd3}
    .badge.err{background:rgba(232,93,77,.2);color:#ffd0d0}
    .badge.idle{background:rgba(154,170,154,.2);color:#e0e6e0}
  </style>
</head>
<body>
  <div class="wrap">
    <div class="card">
      <h1>Stalkerhek Info</h1>
      <div class="sub">Uptime: {{.Uptime}} | Go {{.GoVersion}} | {{.NumGo}} goroutines</div>
    </div>

    <div class="card">
      <h2>System Overview</h2>
      <div class="grid">
        <div class="stat"><div class="stat-label">Host</div><div class="stat-value">{{.Host}}</div></div>
        <div class="stat"><div class="stat-label">Profiles</div><div class="stat-value">{{len .Profiles}}</div></div>
        <div class="stat"><div class="stat-label">Running</div><div class="stat-value ok">{{range .Profiles}}{{if .Running}}1{{end}}{{end}}</div></div>
        <div class="stat"><div class="stat-label">Errors</div><div class="stat-value err">{{range .Profiles}}{{if .Error}}1{{end}}{{end}}</div></div>
      </div>
    </div>

    <div class="card">
      <h2>Profiles</h2>
      <table>
        <thead><tr><th>Name</th><th>Portal</th><th>MAC</th><th>HLS</th><th>Proxy</th><th>Status</th></tr></thead>
        <tbody>
          {{range .Profiles}}
            <tr>
              <td>{{if .Name}}{{.Name}}{{else}}Profile {{.ID}}{{end}}</td>
              <td style="word-break:break-all">{{.PortalURL}}</td>
              <td>{{.MAC}}</td>
              <td>:{{.HlsPort}}</td>
              <td>:{{.ProxyPort}}</td>
              <td>{{if .Running}}<span class="badge ok">Running</span>{{else if .Error}}<span class="badge err">Error</span>{{else}}<span class="badge idle">Idle</span>{{end}}</td>
            </tr>
          {{else}}
            <tr><td colspan="6" style="text-align:center;color:var(--muted)">No profiles configured</td></tr>
          {{end}}
        </tbody>
      </table>
    </div>
  </div>
</body>
</html>`

		t := template.Must(template.New("info").Parse(tpl))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = t.Execute(w, data)
	})
}

// IncrementRequests increments the request counter
func IncrementRequests() {
	healthMu.Lock()
	reqCounter++
	healthMu.Unlock()
}

// IncrementErrors increments the error counter
func IncrementErrors() {
	healthMu.Lock()
	errCounter++
	healthMu.Unlock()
}
