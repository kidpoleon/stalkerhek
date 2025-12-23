package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/url"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/CrazeeGhost/stalkerhek/hls"
	"github.com/CrazeeGhost/stalkerhek/proxy"
	"github.com/CrazeeGhost/stalkerhek/stalker"
)

// Profile represents a user-defined configuration profile
// containing portal credentials and per-profile service ports.
type Profile struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	PortalURL string `json:"portal_url"`
	MAC       string `json:"mac"`
	HlsPort   int    `json:"hls_port"`
	ProxyPort int    `json:"proxy_port"`
}

var (
	profMu      sync.RWMutex
	profiles    = make([]Profile, 0, 8)
	nextProfile = 1
)

const defaultPortalURL = "http://<HOST>/portal.php"

// StartProfileServices launches authentication, channel retrieval, and HLS/Proxy services for a single profile in its own goroutine.
func StartProfileServices(p Profile) {
	log.Printf("[PROFILE %s] Starting services...", p.Name)
	SetProfileValidating(p.ID, p.Name, "Connecting...")
	// Build per-profile config
	cfg := &stalker.Config{
		Portal: &stalker.Portal{
			Model:        "MAG254",
			SerialNumber: "0000000000000",
			DeviceID:     strings.Repeat("f", 64),
			DeviceID2:    strings.Repeat("f", 64),
			Signature:    strings.Repeat("f", 64),
			TimeZone:     "UTC",
			DeviceIdAuth: true,
			WatchDogTime: 5,
			Location:     p.PortalURL,
			MAC:          p.MAC,
		},
		HLS: struct {
			Enabled bool   `yaml:"enabled"`
			Bind    string `yaml:"bind"`
		}{Enabled: true, Bind: fmt.Sprintf("0.0.0.0:%d", p.HlsPort)},
		Proxy: struct {
			Enabled bool   `yaml:"enabled"`
			Bind    string `yaml:"bind"`
			Rewrite bool   `yaml:"rewrite"`
		}{Enabled: true, Bind: fmt.Sprintf("0.0.0.0:%d", p.ProxyPort), Rewrite: true},
	}
	// Authenticate
	if err := cfg.Portal.Start(); err != nil {
		SetProfileError(p.ID, p.Name, err.Error())
		log.Printf("[PROFILE %s] Authentication failed: %v", p.Name, err)
		return
	}
	SetProfileValidating(p.ID, p.Name, "Retrieving channels...")
	// Retrieve channels
	chs, err := cfg.Portal.RetrieveChannels()
	if err != nil {
		SetProfileError(p.ID, p.Name, err.Error())
		log.Printf("[PROFILE %s] Channel retrieval failed: %v", p.Name, err)
		return
	}
	if len(chs) == 0 {
		SetProfileError(p.ID, p.Name, "no IPTV channels retrieved")
		log.Printf("[PROFILE %s] No channels retrieved", p.Name)
		return
	}
	SetProfileSuccess(p.ID, p.Name, len(chs), "", "", true)
	log.Printf("[PROFILE %s] Retrieved %d channels", p.Name, len(chs))

	// Create per-profile context
	pCtx, pCancel := context.WithCancel(context.Background())
	RegisterRunner(p.ID, pCancel)

	// Start HLS
	go func(channels map[string]*stalker.Channel) {
		log.Printf("[PROFILE %s] Starting HLS service on %s", p.Name, cfg.HLS.Bind)
		hls.StartWithContext(pCtx, channels, cfg.HLS.Bind)
		log.Printf("[PROFILE %s] HLS service stopped on %s", p.Name, cfg.HLS.Bind)
	}(chs)

	// Start Proxy
	go func(channels map[string]*stalker.Channel) {
		log.Printf("[PROFILE %s] Starting proxy service on %s", p.Name, cfg.Proxy.Bind)
		proxy.StartWithContext(pCtx, cfg, channels)
		log.Printf("[PROFILE %s] Proxy service stopped on %s", p.Name, cfg.Proxy.Bind)
	}(chs)
}

func normalizePortalURL(in string) string {
	s := strings.TrimSpace(in)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(s), "http://") && !strings.HasPrefix(strings.ToLower(s), "https://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u == nil {
		return s
	}

	path := strings.TrimSpace(u.Path)
	if path == "" || path == "/" {
		path = "/portal.php"
	}
	lower := strings.ToLower(path)
	// Force compatibility: /portal.php yields best results.
	if strings.HasSuffix(lower, "/load.php") {
		path = strings.TrimSuffix(path, "/load.php") + "/portal.php"
		lower = strings.ToLower(path)
	}
	// If user pasted some other php endpoint, override to the canonical one.
	if strings.HasSuffix(lower, ".php") && !strings.HasSuffix(lower, "/portal.php") {
		path = "/portal.php"
		lower = strings.ToLower(path)
	}
	// If user pasted a directory path, append portal.php.
	if !strings.HasSuffix(lower, "/portal.php") {
		if strings.HasSuffix(path, "/") {
			path = path + "portal.php"
		} else {
			path = strings.TrimRight(path, "/") + "/portal.php"
		}
	}
	u.Path = path
	return u.String()
}

// AddProfile appends a new profile to memory and returns it.
func AddProfile(p Profile) Profile {
	profMu.Lock()
	defer profMu.Unlock()
	p.ID = nextProfile
	nextProfile++
	profiles = append(profiles, p)
	return p
}

// ListProfiles returns a copy of current profiles.
func ListProfiles() []Profile {
	profMu.RLock()
	defer profMu.RUnlock()
	out := make([]Profile, len(profiles))
	copy(out, profiles)
	return out
}

// RegisterProfileHandlers mounts profile CRUD and control endpoints.
func RegisterProfileHandlers(mux *http.ServeMux, onStart func()) {
	mux.HandleFunc("/api/profiles", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ListProfiles())
	})

	// Basic create endpoint supporting form-encoded submissions
	mux.HandleFunc("/profiles", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		portal := normalizePortalURL(r.FormValue("portal"))
		if portal == "" {
			portal = defaultPortalURL
		}
		mac := strings.ToUpper(strings.TrimSpace(r.FormValue("mac")))
		hlsStr := strings.TrimSpace(r.FormValue("hls_port"))
		proxyStr := strings.TrimSpace(r.FormValue("proxy_port"))
		if portal == "" || mac == "" || hlsStr == "" || proxyStr == "" {
			http.Error(w, "portal, mac, hls_port, proxy_port are required", http.StatusBadRequest)
			return
		}
		hlsPort, err1 := strconv.Atoi(hlsStr)
		proxyPort, err2 := strconv.Atoi(proxyStr)
		if err1 != nil || err2 != nil || hlsPort <= 0 || proxyPort <= 0 {
			http.Error(w, "invalid ports", http.StatusBadRequest)
			return
		}
		p := AddProfile(Profile{
			Name:      name,
			PortalURL: portal,
			MAC:       mac,
			HlsPort:   hlsPort,
			ProxyPort: proxyPort,
		})
		_ = SaveProfiles()
		// Immediately start services for this profile in a goroutine
		go StartProfileServices(p)
		// redirect back to dashboard
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		_ = p
	})

	// Start signal: when invoked, the outer caller can proceed to start services.
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if len(ListProfiles()) == 0 {
			http.Error(w, "no profiles defined", http.StatusBadRequest)
			return
		}
		if onStart != nil {
			onStart()
		}
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	})

	// Minimal dashboard HTML if not provided by status.go
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		host := r.Host
		if i := strings.Index(host, ":"); i > -1 {
			host = host[:i]
		}
		data := struct {
			Host     string
			Profiles []Profile
		}{Host: host, Profiles: ListProfiles()}

		const tpl = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Stalkerhek Dashboard</title>
  <style>
    :root{--bg:#0a0f0a;--panel:#0d1410;--panel2:#111815;--border:#1f2e23;--text:#e0e6e0;--muted:#9aaa9a;--brand:#2d7a4e;--brand-hover:#3a8f5e;--ok:#3fb970;--warn:#d4a94a;--bad:#e85d4d}
    *{box-sizing:border-box}
    body{margin:0;font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Helvetica,Arial,sans-serif;background:linear-gradient(180deg, #0d1410 0%, #0a0f0a 100%);color:var(--text);min-height:100vh}
    a{color:var(--brand);text-decoration:none} a:hover{color:var(--brand-hover);text-decoration:underline}
    .wrap{max-width:1200px;margin:0 auto;padding:16px 12px 60px}
    .topbar{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap;margin-bottom:16px}
    .title{display:flex;flex-direction:column;gap:6px}
    h1{margin:0;font-size:28px;letter-spacing:.1px;color:var(--text)}
    .sub{color:var(--muted);font-size:15px;line-height:1.4}
    .pill{display:inline-flex;align-items:center;gap:8px;padding:8px 12px;border:1px solid var(--border);border-radius:999px;background:rgba(31,46,35,.5);color:var(--muted);font-size:13px}
    .grid{display:grid;grid-template-columns:1fr;gap:16px}
    @media(min-width:900px){.grid{grid-template-columns: 1fr}}
    .card{background:linear-gradient(180deg, rgba(17,24,21,.96), rgba(13,20,16,.94));border:1px solid var(--border);border-radius:16px;padding:20px;box-shadow:0 12px 32px rgba(0,0,0,.4)}
    .card h2{margin:0 0 12px 0;font-size:18px;color:var(--text)}
    .step{display:flex;gap:12px;align-items:flex-start;margin:12px 0}
    .num{flex:0 0 auto;width:28px;height:28px;border-radius:8px;background:rgba(45,122,78,.2);border:1px solid rgba(45,122,78,.35);display:grid;place-items:center;color:#a8e0c0;font-weight:700}
    .step p{margin:0;color:var(--muted);font-size:14px;line-height:1.4}
    label{display:block;font-size:13px;color:#c5d1c5;margin:12px 0 6px}
    .hint{font-size:13px;color:var(--muted);margin-top:8px;line-height:1.4}
    .row{display:grid;grid-template-columns:1fr;gap:12px}
    @media(min-width:520px){.row.two{grid-template-columns:1fr 1fr}}
    input{width:100%;padding:14px 14px;border-radius:12px;border:1px solid var(--border);background:#0f1612;color:var(--text);outline:none;font-size:16px;transition:border-color .2s,box-shadow .2s}
    input:focus{border-color:var(--brand);box-shadow:0 0 0 3px rgba(45,122,78,.2)}
    .err{display:none;margin-top:6px;color:var(--bad);font-size:12px}
    .btnbar{display:flex;gap:12px;flex-wrap:wrap;margin-top:16px}
    button{cursor:pointer;border:none;border-radius:12px;padding:14px 16px;font-size:15px;font-weight:650;transition:background .2s,filter .2s}
    .primary{background:var(--brand);color:white}
    .primary:hover{background:var(--brand-hover);filter:brightness(1.05)}
    .ghost{background:transparent;border:1px solid var(--border);color:var(--text)}
    .ghost:hover{background:rgba(45,122,78,.1);border-color:var(--brand)}
    .danger{background:rgba(232,93,77,.14);border:1px solid rgba(232,93,77,.35);color:#ffd0d0}
    .danger:hover{background:rgba(232,93,77,.2)}
    .ok{background:rgba(63,185,112,.14);border:1px solid rgba(63,185,112,.25);color:#d9ffe6}
    .ok:hover{background:rgba(63,185,112,.2)}
    .profiles{display:grid;grid-template-columns:1fr;gap:14px}
    .p{padding:16px;border-radius:14px;border:1px solid var(--border);background:rgba(13,20,16,.8)}
    .phead{display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap;align-items:center}
    .pname{font-weight:800;color:var(--text)}
    .badg{font-size:12px;padding:6px 10px;border-radius:999px;border:1px solid var(--border);color:var(--muted)}
    .badg.ok{border-color:rgba(63,185,112,.3);color:#bfffd3}
    .badg.err{border-color:rgba(232,93,77,.35);color:#ffd0d0}
    .badg.run{border-color:rgba(45,122,78,.35);color:#d7e6ff}
    .meta{margin-top:12px;color:var(--muted);font-size:13px;line-height:1.4;display:grid;gap:6px}
    .links{display:flex;gap:10px;flex-wrap:wrap;margin-top:12px}
    .actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:14px}
    .footnote{margin-top:12px;color:var(--muted);font-size:12px;line-height:1.4}
    .toast{position:fixed;right:16px;bottom:16px;max-width:520px;background:rgba(13,20,16,.92);border:1px solid var(--border);border-radius:14px;padding:12px 12px;display:none;box-shadow:0 16px 40px rgba(0,0,0,.45)}
    .toast strong{display:block;margin-bottom:4px}
  </style>
</head>
<body>
  <div class="wrap">
    <div class="topbar">
      <div class="title">
        <h1>Stalkerhek Dashboard</h1>
        <div class="sub">This page is the only page you need. Follow the steps, press <b>Verify</b>, then <b>Start Streaming</b>. Links appear automatically.</div>
      </div>
      <div class="pill" title="Your device will access HLS/Proxy on this host">
        Host:
        <b>{{.Host}}</b>
      </div>
    </div>

    <div class="grid">
      <div class="card">
        <h2>Add a Profile</h2>
        <div class="step"><div class="num">1</div><p><b>Portal URL</b> should be the full <code>portal.php</code> URL. Example: <code>http://&lt;HOST&gt;/portal.php</code></p></div>
        <div class="step"><div class="num">2</div><p><b>MAC</b> must be uppercase and include colons. Example: <code>00:1A:79:12:34:56</code></p></div>
        <div class="step"><div class="num">3</div><p><b>Ports</b> must be unique per profile if you run more than one.</p></div>

        <form id="addForm" method="post" action="/profiles" novalidate>
          <label for="name">Profile name (optional)</label>
          <input id="name" name="name" placeholder="Living Room / Office / Backup" title="Give it a friendly name so you can recognize it" />

          <label for="portal">Portal URL (required)</label>
          <input id="portal" name="portal" required placeholder="http://example.com/portal.php" title="Paste your portal URL; the UI will autocorrect it to /portal.php" />
          <div id="portalErr" class="err">Please enter a valid URL. We'll autocorrect to <b>/portal.php</b>.</div>

          <label for="mac">MAC address (required)</label>
          <input id="mac" name="mac" required placeholder="00:1A:79:12:34:56" title="Must be uppercase with colons" />
          <div id="macErr" class="err">MAC must look like <b>00:1A:79:12:34:56</b>.</div>

          <div class="row two">
            <div>
              <label for="hls_port">HLS Port</label>
              <input id="hls_port" name="hls_port" required inputmode="numeric" title="This port serves the playlist and streams" />
            </div>
            <div>
              <label for="proxy_port">Proxy Port</label>
              <input id="proxy_port" name="proxy_port" required inputmode="numeric" title="This port is used by STB-style apps" />
            </div>
          </div>

          <div class="btnbar">
            <button class="primary" type="submit" title="Saves profile to the list below">Save Profile</button>
            <button class="ghost" type="button" id="fillDemo" title="Fills example values so you can see what is expected">Show Example</button>
          </div>
          <div class="hint">Tip: After saving, the profile will start automatically. Links will appear below once ready.</div>
        </form>
      </div>

    <div style="height:14px"></div>

    <div class="card">
      <h2>Your Profiles</h2>
      <div id="profiles" class="profiles">
        {{range .Profiles}}
          <div class="p" data-id="{{.ID}}">
            <div class="phead">
              <div>
                <div class="pname">{{if .Name}}{{.Name}}{{else}}Profile {{.ID}}{{end}}</div>
                <div class="sub" style="margin-top:4px">Portal: <span style="color:#c5d1c5">{{.PortalURL}}</span></div>
                <div class="sub">MAC: <span style="color:#c5d1c5">{{.MAC}}</span></div>
              </div>
              <div class="badg" id="badge-{{.ID}}" title="Current status of this profile">Idle</div>
            </div>

            <div class="links">
              <a id="hls-{{.ID}}" href="http://{{$.Host}}:{{.HlsPort}}/" target="_blank" title="Open HLS endpoint">HLS: :{{.HlsPort}}</a>
              <a id="pxy-{{.ID}}" href="http://{{$.Host}}:{{.ProxyPort}}/" target="_blank" title="Open Proxy endpoint">Proxy: :{{.ProxyPort}}</a>
            </div>

            <div class="actions">
              <form method="post" action="/profiles/verify" style="margin:0" title="Checks credentials and counts channels (does not start streaming)">
                <input type="hidden" name="id" value="{{.ID}}" />
                <button class="ok" type="submit" title="Verify this profile">Verify</button>
              </form>
              <form method="post" action="/profiles/stop" style="margin:0" onsubmit="return confirm('Stop this profile? Streams will stop immediately.')" title="Stops streaming for this profile">
                <input type="hidden" name="id" value="{{.ID}}" />
                <button class="ghost" type="submit" title="Stop this profile">Stop</button>
              </form>
              <form method="post" action="/profiles/delete" style="margin:0" onsubmit="return confirm('Delete this profile? This cannot be undone.')" title="Removes this profile from the list">
                <input type="hidden" name="id" value="{{.ID}}" />
                <button class="danger" type="submit" title="Delete this profile">Delete</button>
              </form>
            </div>
            <div class="meta" id="meta-{{.ID}}" title="Detailed status and channel count"></div>
          </div>
        {{else}}
          <div class="p">
            <div class="pname">No profiles yet</div>
            <div class="sub" style="margin-top:6px">Use <b>Add a Profile</b> to create your first profile.</div>
          </div>
        {{end}}
      </div>
    </div>
  </div>

  <div id="toast" class="toast"><strong id="toastTitle"></strong><div id="toastMsg"></div></div>

  <script>
    const macRe = /^[0-9A-F]{2}(:[0-9A-F]{2}){5}$/;
    function normalizePortal(raw){
      let s = (raw||'').trim();
      if(!s) return '';
      if(!/^https?:\/\//i.test(s)) s = 'http://' + s;
      try{
        const u = new URL(s);
        let p = (u.pathname||'/').trim();
        if(!p || p === '/') p = '/portal.php';
        if(/\/load\.php$/i.test(p)) p = p.replace(/\/load\.php$/i, '/portal.php');
        if(!/\/portal\.php$/i.test(p)){
          if(/\.php$/i.test(p)) p = '/portal.php';
          else p = (p.replace(/\/+$/,'') || '') + '/portal.php';
        }
        u.pathname = p;
        return u.toString();
      }catch(e){
        return s;
      }
    }
    function showToast(title, msg){
      const t=document.getElementById('toast');
      document.getElementById('toastTitle').textContent=title;
      document.getElementById('toastMsg').textContent=msg;
      t.style.display='block';
      clearTimeout(window.__toastTimer);
      window.__toastTimer=setTimeout(()=>t.style.display='none', 3800);
    }
    function validate(){
      const portal=document.getElementById('portal');
      const mac=document.getElementById('mac');
      const portalErr=document.getElementById('portalErr');
      const macErr=document.getElementById('macErr');
      let ok=true;
      const v=normalizePortal(portal.value||'');
      portal.value=v;
      const m=(mac.value||'').trim().toUpperCase();
      mac.value=m;
      const portalOk = /^https?:\/\//i.test(v) && /portal\.php(\?.*)?$/i.test(v);
      if(!portalOk){ portalErr.style.display='block'; ok=false } else portalErr.style.display='none';
      if(!macRe.test(m)){ macErr.style.display='block'; ok=false } else macErr.style.display='none';
      return ok;
    }
    document.getElementById('addForm').addEventListener('submit', (e)=>{
      if(!validate()){
        e.preventDefault();
        showToast('Fix required fields', 'Please correct Portal URL and MAC format, then try again.');
      }
    });
    document.getElementById('fillDemo').addEventListener('click', ()=>{
      document.getElementById('portal').value='http://example.com/portal.php';
      document.getElementById('mac').value='00:1A:79:12:34:56';
      showToast('Example filled', 'Replace with your real portal URL and MAC.');
    });

    async function poll(){
      try{
        const r = await fetch('/api/profile_status', {cache:'no-store'});
        const a = await r.json();
        for(const s of a){
          const badge=document.getElementById('badge-'+s.id);
          const meta=document.getElementById('meta-'+s.id);
          if(!badge || !meta) continue;
          badge.className='badg';
          if(s.phase==='success') badge.classList.add('ok');
          if(s.phase==='error') badge.classList.add('err');
          if(s.running) badge.classList.add('run');
          const label = s.running ? 'Running' : (s.phase==='success' ? 'Verified' : (s.phase==='error' ? 'Error' : (s.phase==='validating' ? 'Checking…' : 'Idle')));
          badge.textContent = label;
          let lines=[];
          if(s.message) lines.push(s.message);
          if(s.channels) lines.push('Channels: '+s.channels);
          if(lines.length===0) lines.push('');
          meta.innerHTML = '<div>'+lines.map(x=>String(x).replace(/</g,'&lt;')).join('</div><div>')+'</div>';
          if(s.hls){ const h=document.getElementById('hls-'+s.id); if(h){ h.href=s.hls; h.textContent='HLS: '+s.hls; } }
          if(s.proxy){ const p=document.getElementById('pxy-'+s.id); if(p){ p.href=s.proxy; p.textContent='Proxy: '+s.proxy; } }
        }
      }catch(e){}
    }
    setInterval(poll, 1200);
    poll();
  </script>
</body>
</html>`

		t := template.Must(template.New("dash").Parse(tpl))
		_ = t.Execute(w, data)
	})
}

// GetProfile returns a profile by ID
func GetProfile(id int) (Profile, bool) {
    profMu.RLock()
    defer profMu.RUnlock()
    for _, p := range profiles {
        if p.ID == id { return p, true }
    }
    return Profile{}, false
}

// DeleteProfile removes a profile by ID
func DeleteProfile(id int) {
    profMu.Lock()
    defer profMu.Unlock()
    out := make([]Profile, 0, len(profiles))
    for _, p := range profiles {
        if p.ID != id { out = append(out, p) }
    }
    profiles = out
}
