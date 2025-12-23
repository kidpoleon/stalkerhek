package webui

import (
    "context"
    "html/template"
    "log"
    "net/http"
    "strings"
    "time"

    "github.com/CrazeeGhost/stalkerhek/stalker"
)

type uiState struct {
    PortalURL string
    MAC       string
}

func StartWithContext(ctx context.Context, cfg *stalker.Config, ready chan struct{}) {
    mux := http.NewServeMux()

	/* load persisted profiles if any */
	_ = LoadProfiles()

    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/" {
            http.NotFound(w, r)
            return
        }
        http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
    })

    mux.HandleFunc("/setup", func(w http.ResponseWriter, r *http.Request) {
        st := uiState{PortalURL: cfg.Portal.Location, MAC: cfg.Portal.MAC}
        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        render(w, st)
    })

    // mount status endpoints
    RegisterStatusHandlers(mux)

    // mount per-profile status endpoints (verify/stop/delete and JSON feed)
    RegisterProfileStatusHandlers(mux)

    // mount health/metrics/info endpoints
    RegisterHealthHandlers(mux)

    // middleware to count requests/errors
    var handler http.Handler = mux
    handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        IncrementRequests()
        mux.ServeHTTP(w, r)
    })

    // mount profile endpoints, signal readiness when /start is called
    RegisterProfileHandlers(mux, func() {
        select {
        case <-ready:
        default:
            close(ready)
        }
    })

    mux.HandleFunc("/save", func(w http.ResponseWriter, r *http.Request) {
        if err := r.ParseForm(); err != nil {
            http.Error(w, "bad request", http.StatusBadRequest)
            return
        }
        portal := strings.TrimSpace(r.FormValue("portal"))
        mac := strings.ToUpper(strings.TrimSpace(r.FormValue("mac")))
        if portal == "" || mac == "" {
            http.Error(w, "portal and mac are required", http.StatusBadRequest)
            return
        }

        cfg.Portal.Location = portal
        cfg.Portal.MAC = mac
        cfg.Portal.TimeZone = "UTC"
        cfg.Portal.DeviceIdAuth = true

        cfg.HLS.Enabled = true
        cfg.HLS.Bind = "0.0.0.0:7770"
        cfg.Proxy.Enabled = true
        cfg.Proxy.Bind = "0.0.0.0:7700"
        cfg.Proxy.Rewrite = true

        // Signal readiness once (non-blocking if already closed)
        select {
        case <-ready:
            // already ready
        default:
            close(ready)
        }
        http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
    })

    srv := &http.Server{Addr: ":4400", Handler: handler}

    go func() {
        <-ctx.Done()
        log.Println("WebUI shutdown: draining new requests for 3 seconds...")
        time.Sleep(3 * time.Second)
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        if err := srv.Shutdown(shutdownCtx); err != nil {
            log.Printf("WebUI shutdown error: %v", err)
        } else {
            log.Println("WebUI shutdown complete")
        }
    }()

    go func() {
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Printf("webui error: %v", err)
        }
    }()
}

func render(w http.ResponseWriter, st uiState) {
    const tpl = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Stalkerhek Setup</title><style>body{margin:0;font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Helvetica,Arial,sans-serif;background:#0b1020;color:#e6e6e6;display:flex;align-items:center;justify-content:center;min-height:100vh} .card{background:#141a2a;border:1px solid #25304b;border-radius:12px;padding:28px;box-shadow:0 10px 30px rgba(0,0,0,.4);width:min(520px,92vw)} h1{margin:0 0 18px 0;font-size:20px} .row{display:flex;flex-direction:column;margin-bottom:14px} label{font-size:12px;margin-bottom:6px;color:#aab4ce} input{background:#0e1324;border:1px solid #2b3a5a;border-radius:8px;padding:12px;color:#e6e6e6;outline:none} input:focus{border-color:#5b8def;box-shadow:0 0 0 3px rgba(91,141,239,.15)} .hint{font-size:12px;color:#8c96b2;margin-top:2px} .actions{display:flex;gap:10px;justify-content:flex-end;margin-top:10px} button{background:#5b8def;color:white;border:none;border-radius:8px;padding:12px 16px;cursor:pointer} button:hover{background:#6d9af0} .grid{display:grid;grid-template-columns:1fr 1fr;gap:10px} .muted{color:#8c96b2;font-size:12px}</style></head><body><div class="card"><h1>Stalkerhek Setup</h1><form method="post" action="/save"><div class="row"><label for="portal">Portal URL</label><input id="portal" name="portal" type="url" required placeholder="http://domain.example.com/stalker_portal/server/load.php" value="{{.PortalURL}}"><div class="hint">Required</div></div><div class="row"><label for="mac">MAC Address</label><input id="mac" name="mac" type="text" required placeholder="00:1A:79:00:00:00" value="{{.MAC}}"><div class="hint">Required (uppercase)</div></div><div class="grid"><div class="row"><label>HLS</label><div class="muted">Enabled on 7770</div></div><div class="row"><label>Proxy</label><div class="muted">Enabled on 7700</div></div></div><div class="row"><div class="muted">Timezone defaults to UTC. Device ID auth enabled. Other values remain unchanged.</div></div><div class="actions"><button type="submit">Save</button></div></form></div></body></html>`
    t := template.Must(template.New("p").Parse(tpl))
    _ = t.Execute(w, st)
}

// no file persistence; configuration remains in-memory for the process lifetime
