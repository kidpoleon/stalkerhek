package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/url"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kidpoleon/stalkerhek/hls"
	"github.com/kidpoleon/stalkerhek/proxy"
	"github.com/kidpoleon/stalkerhek/stalker"
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
	// Advanced / portal auth fields
	Model        string `json:"model,omitempty"`
	SerialNumber string `json:"serial_number,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	DeviceID2    string `json:"device_id2,omitempty"`
	Signature    string `json:"signature,omitempty"`
	TimeZone     string `json:"time_zone,omitempty"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	WatchDogTime int    `json:"watchdog_time,omitempty"`
}

func profileWithDefaults(p Profile) Profile {
	if p.Model == "" {
		p.Model = "MAG254"
	}
	if p.SerialNumber == "" {
		p.SerialNumber = "0000000000000"
	}
	if p.DeviceID == "" {
		p.DeviceID = strings.Repeat("f", 64)
	}
	if p.DeviceID2 == "" {
		p.DeviceID2 = strings.Repeat("f", 64)
	}
	if p.Signature == "" {
		p.Signature = strings.Repeat("f", 64)
	}
	if p.TimeZone == "" {
		p.TimeZone = "UTC"
	}
	if p.WatchDogTime == 0 {
		p.WatchDogTime = 5
	}
	return p
}

var macRe = regexp.MustCompile(`^([0-9A-F]{2}:){5}[0-9A-F]{2}$`)

func isValidMAC(mac string) bool {
	mac = strings.ToUpper(strings.TrimSpace(mac))
	return macRe.MatchString(mac)
}

func normalizePortalURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(s), "http://") && !strings.HasPrefix(strings.ToLower(s), "https://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/portal.php"
	}
	if !strings.HasSuffix(strings.ToLower(u.Path), "/portal.php") && !strings.HasSuffix(strings.ToLower(u.Path), "/load.php") {
		// If a random .php is given, portal.php is usually required in the same directory.
		if strings.HasSuffix(strings.ToLower(u.Path), ".php") {
			u.Path = path.Join(path.Dir(u.Path), "portal.php")
		} else {
			u.Path = strings.TrimSuffix(u.Path, "/") + "/portal.php"
		}
	}
	return u.String()
}

func normalizePortalURLWithNotice(raw string) (string, bool) {
	n := normalizePortalURL(raw)
	return n, strings.TrimSpace(raw) != "" && strings.TrimSpace(raw) != n
}

func checkPortAvailable(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("invalid port: %d", port)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	_ = ln.Close()
	return nil
}

func isLikelyValidPortalURL(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if strings.TrimSpace(u.Host) == "" {
		return false
	}
	lp := strings.ToLower(u.Path)
	return strings.HasSuffix(lp, "/portal.php") || strings.HasSuffix(lp, "/load.php")
}

func friendlyStartError(err error) string {
	if err == nil {
		return ""
	}
	e := strings.TrimSpace(err.Error())
	low := strings.ToLower(e)

	if strings.Contains(low, "invalid credentials") {
		return "Login failed. Likely wrong Portal URL or MAC address. Tip: portal should end with /portal.php and MAC must match your provider. Open Logs for details."
	}
	if strings.Contains(low, "portal returned no channel data") || strings.Contains(low, "no channels returned") {
		return "No channels returned. This is usually a wrong MAC address or wrong Portal URL. Tip: use the exact provider URL ending in /portal.php and verify the MAC is correct. Open Logs for details."
	}
	if strings.Contains(low, "timed out") {
		return "Connection timed out. Check Portal URL, internet, and whether the portal is reachable from this machine. Open Logs for details."
	}
	if strings.Contains(low, "returned 401") || strings.Contains(low, "returned 403") {
		return "Portal rejected the request (401/403). This is usually a wrong Portal URL, blocked MAC, or the provider is restricting access. Open Logs for details."
	}
	if strings.Contains(low, "no such host") || strings.Contains(low, "name resolution") {
		return "Portal hostname could not be resolved. Check Portal URL spelling and DNS/network. Open Logs for details."
	}
	return e
}

func externalHost(r *http.Request) string {
	host := r.Host
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xf != "" {
		host = strings.TrimSpace(strings.Split(xf, ",")[0])
	}
	if fwd := strings.TrimSpace(r.Header.Get("Forwarded")); fwd != "" {
		fwd = strings.TrimSpace(strings.Split(fwd, ",")[0])
		parts := strings.Split(fwd, ";")
		for _, p := range parts {
			kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
			if len(kv) != 2 {
				continue
			}
			k := strings.ToLower(strings.TrimSpace(kv[0]))
			v := strings.Trim(strings.TrimSpace(kv[1]), "\"")
			if k == "host" && v != "" {
				host = v
			}
		}
	}
	if i := strings.Index(host, ":"); i > -1 {
		host = host[:i]
	}
	return host
}

var (
	profMu      sync.RWMutex
	profiles    = make([]Profile, 0, 8)
	nextProfile = 1
	startMu     sync.Mutex
	startBusy   = map[int]bool{}
	stopRequested = map[int]bool{}
)

const defaultPortalURL = "http://<HOST>/portal.php"

// StartProfileServices launches authentication, channel retrieval, and HLS/Proxy services for a single profile in its own goroutine.
func StartProfileServices(p Profile) {
	startMu.Lock()
	if startBusy[p.ID] {
		startMu.Unlock()
		AppendProfileLog(p.ID, "Start ignored (already starting)")
		return
	}
	startBusy[p.ID] = true
	stopRequested[p.ID] = false
	startMu.Unlock()
	defer func() {
		startMu.Lock()
		delete(startBusy, p.ID)
		delete(stopRequested, p.ID)
		startMu.Unlock()
	}()
	shouldStop := func(stage string) bool {
		startMu.Lock()
		sr := stopRequested[p.ID]
		if sr {
			stopRequested[p.ID] = false
		}
		startMu.Unlock()
		if sr {
			AppendProfileLog(p.ID, "Stop requested; aborting ("+stage+")")
			SetProfileStopped(p.ID)
			return true
		}
		return false
	}

	// Preflight validation (server-side) so UI is idiot-proof even if the client JS is bypassed.
	if norm, changed := normalizePortalURLWithNotice(p.PortalURL); changed {
		AppendProfileLog(p.ID, "Portal URL normalized")
		p.PortalURL = norm
	} else {
		p.PortalURL = normalizePortalURL(p.PortalURL)
	}
	p.MAC = strings.ToUpper(strings.TrimSpace(p.MAC))
	if !isLikelyValidPortalURL(p.PortalURL) {
		AppendProfileLog(p.ID, "Invalid portal URL: "+p.PortalURL)
		SetProfileError(p.ID, p.Name, "Invalid Portal URL. Tip: it should look like http(s)://HOST/portal.php or /load.php (some providers use /stalker_portal/portal.php).")
		return
	}
	if !isValidMAC(p.MAC) {
		AppendProfileLog(p.ID, "Invalid MAC format")
		SetProfileError(p.ID, p.Name, "Invalid MAC format. Tip: it must be 6 hex pairs like 00:1A:79:AA:BB:CC.")
		return
	}
	if err := checkPortAvailable(p.HlsPort); err != nil {
		AppendProfileLog(p.ID, "HLS port unavailable")
		SetProfileError(p.ID, p.Name, "HLS port is unavailable. Choose a different HLS port.")
		return
	}
	if err := checkPortAvailable(p.ProxyPort); err != nil {
		AppendProfileLog(p.ID, "Proxy port unavailable")
		SetProfileError(p.ID, p.Name, "Proxy port is unavailable. Choose a different Proxy port.")
		return
	}

	log.Printf("[PROFILE %s] Starting services...", p.Name)
	AppendProfileLog(p.ID, "Starting services")
	SetProfileValidating(p.ID, p.Name, "Connecting... (attempt 1/3)")
	AppendProfileLog(p.ID, "Connecting to portal")
	// Build per-profile config
	pd := profileWithDefaults(p)
	deviceIdAuth := pd.Username == "" && pd.Password == ""
	cfg := &stalker.Config{
		Portal: &stalker.Portal{
			Model:        pd.Model,
			SerialNumber: pd.SerialNumber,
			DeviceID:     pd.DeviceID,
			DeviceID2:    pd.DeviceID2,
			Signature:    pd.Signature,
			TimeZone:     pd.TimeZone,
			DeviceIdAuth: deviceIdAuth,
			WatchDogTime: pd.WatchDogTime,
			Location:     pd.PortalURL,
			MAC:          pd.MAC,
			Username:     pd.Username,
			Password:     pd.Password,
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
	// Authenticate (soft timeout: 3 tries)
	{
		const maxAttempts = 3
		const perAttemptTimeout = 20 * time.Second
		var lastErr error
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			if shouldStop("before authentication") {
				return
			}
			SetProfileValidating(p.ID, p.Name, fmt.Sprintf("Connecting... (attempt %d/%d)", attempt, maxAttempts))
			AppendProfileLog(p.ID, fmt.Sprintf("Auth attempt %d/%d", attempt, maxAttempts))
			errCh := make(chan error, 1)
			go func() { errCh <- cfg.Portal.Start() }()
			select {
			case err := <-errCh:
				if err == nil {
					lastErr = nil
					attempt = maxAttempts
					break
				}
				lastErr = err
				AppendProfileLog(p.ID, "Authentication failed: "+err.Error())
			case <-time.After(perAttemptTimeout):
				lastErr = fmt.Errorf("authentication timed out after %s", perAttemptTimeout)
				AppendProfileLog(p.ID, lastErr.Error())
			}
			if lastErr == nil {
				break
			}
			if shouldStop("after authentication attempt") {
				return
			}
			if attempt < maxAttempts {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
		}
		if lastErr != nil {
			SetProfileError(p.ID, p.Name, friendlyStartError(lastErr))
			log.Printf("[PROFILE %s] Authentication failed: %v", p.Name, lastErr)
			return
		}
	}
	if shouldStop("after authentication") {
		return
	}
	SetProfileValidating(p.ID, p.Name, "Retrieving channels...")
	AppendProfileLog(p.ID, "Authentication OK")
	AppendProfileLog(p.ID, "Retrieving channels")
	// Retrieve channels (soft timeout: 3 tries)
	chs := map[string]*stalker.Channel{}
	{
		const maxAttempts = 3
		const perAttemptTimeout = 30 * time.Second
		var lastErr error
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			if shouldStop("before channel retrieval") {
				return
			}
			SetProfileValidating(p.ID, p.Name, fmt.Sprintf("Retrieving channels... (attempt %d/%d)", attempt, maxAttempts))
			AppendProfileLog(p.ID, fmt.Sprintf("Retrieve channels attempt %d/%d", attempt, maxAttempts))
			type result struct {
				chs map[string]*stalker.Channel
				err error
			}
			resCh := make(chan result, 1)
			go func() {
				c, err := cfg.Portal.RetrieveChannels()
				resCh <- result{chs: c, err: err}
			}()
			select {
			case res := <-resCh:
				if res.err == nil {
					chs = res.chs
					lastErr = nil
					attempt = maxAttempts
					break
				}
				lastErr = res.err
				AppendProfileLog(p.ID, "Channel retrieval failed: "+res.err.Error())
			case <-time.After(perAttemptTimeout):
				lastErr = fmt.Errorf("channel retrieval timed out after %s", perAttemptTimeout)
				AppendProfileLog(p.ID, lastErr.Error())
			}
			if lastErr == nil {
				break
			}
			if shouldStop("after channel retrieval attempt") {
				return
			}
			if attempt < maxAttempts {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
		}
		if lastErr != nil {
			SetProfileError(p.ID, p.Name, friendlyStartError(lastErr))
			log.Printf("[PROFILE %s] Channel retrieval failed: %v", p.Name, lastErr)
			return
		}
	}
	if shouldStop("after channel retrieval") {
		return
	}
	if len(chs) == 0 {
		AppendProfileLog(p.ID, "No IPTV channels retrieved")
		SetProfileError(p.ID, p.Name, friendlyStartError(fmt.Errorf("no channels returned")))
		log.Printf("[PROFILE %s] No channels retrieved", p.Name)
		return
	}
	SetProfileSuccess(p.ID, p.Name, len(chs), "", "", true)
	AppendProfileLog(p.ID, fmt.Sprintf("Retrieved %d channels", len(chs)))
	if shouldStop("before starting services") {
		return
	}

	// Summary for Fetch step widget.
	// Keep it light and robust: best-effort sampling.
	{
		// sample channel names
		names := make([]string, 0, len(chs))
		catSet := map[string]struct{}{}
		for k, ch := range chs {
			nm := strings.TrimSpace(k)
			if nm == "" && ch != nil {
				nm = strings.TrimSpace(ch.Title)
			}
			if nm != "" {
				names = append(names, nm)
			}
			if ch != nil {
				g := strings.TrimSpace(ch.Genre())
				if g != "" {
					catSet[g] = struct{}{}
				}
			}
		}
		sort.Strings(names)
		sampleChannels := names
		if len(sampleChannels) > 5 {
			sampleChannels = sampleChannels[:5]
		}
		cats := make([]string, 0, len(catSet))
		for c := range catSet {
			cats = append(cats, c)
		}
		sort.Strings(cats)
		sampleCats := cats
		if len(sampleCats) > 6 {
			sampleCats = sampleCats[:6]
		}
		SetProfileSummary(p.ID, cfg.Portal.TimeZone, sampleChannels, len(catSet), sampleCats)
	}
	log.Printf("[PROFILE %s] Retrieved %d channels", p.Name, len(chs))

	// Create per-profile context
	pCtx, pCancel := context.WithCancel(context.Background())
	RegisterRunner(p.ID, pCancel)
	done := make(chan struct{})
	RegisterRunnerDone(p.ID, done)
	var live atomic.Int32
	live.Store(2)
	markDone := func() {
		if live.Add(-1) == 0 {
			close(done)
			ClearProfileChannels(p.ID)
		}
	}

	// Start HLS
	go func(channels map[string]*stalker.Channel) {
		log.Printf("[PROFILE %s] Starting HLS service on %s", p.Name, cfg.HLS.Bind)
		AppendProfileLog(p.ID, "Starting HLS on "+cfg.HLS.Bind)
		SetProfileChannels(p.ID, channels)
		hls.StartWithContext(pCtx, p.ID, channels, cfg.HLS.Bind)
		log.Printf("[PROFILE %s] HLS service stopped on %s", p.Name, cfg.HLS.Bind)
		AppendProfileLog(p.ID, "HLS stopped")
		markDone()
	}(chs)

	// Start Proxy
	go func(channels map[string]*stalker.Channel) {
		log.Printf("[PROFILE %s] Starting proxy service on %s", p.Name, cfg.Proxy.Bind)
		AppendProfileLog(p.ID, "Starting Proxy on "+cfg.Proxy.Bind)
		proxy.StartWithContext(pCtx, p.ID, cfg, channels)
		log.Printf("[PROFILE %s] Proxy service stopped on %s", p.Name, cfg.Proxy.Bind)
		AppendProfileLog(p.ID, "Proxy stopped")
		markDone()
	}(chs)

}

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
		editIDStr := strings.TrimSpace(r.FormValue("edit_id"))
		name := strings.TrimSpace(r.FormValue("name"))
		portal := normalizePortalURL(r.FormValue("portal"))
		if norm, changed := normalizePortalURLWithNotice(r.FormValue("portal")); changed {
			portal = norm
		}
		if portal == "" {
			portal = defaultPortalURL
		}
		mac := strings.ToUpper(strings.TrimSpace(r.FormValue("mac")))
		hlsStr := strings.TrimSpace(r.FormValue("hls_port"))
		proxyStr := strings.TrimSpace(r.FormValue("proxy_port"))
		model := strings.TrimSpace(r.FormValue("model"))
		serialNumber := strings.TrimSpace(r.FormValue("serial_number"))
		deviceID := strings.TrimSpace(r.FormValue("device_id"))
		deviceID2 := strings.TrimSpace(r.FormValue("device_id2"))
		signature := strings.TrimSpace(r.FormValue("signature"))
		timezone := strings.TrimSpace(r.FormValue("timezone"))
		username := strings.TrimSpace(r.FormValue("username"))
		password := strings.TrimSpace(r.FormValue("password"))
		watchdogStr := strings.TrimSpace(r.FormValue("watchdog_time"))
		watchdog, _ := strconv.Atoi(watchdogStr)
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
		if !isValidMAC(mac) {
			http.Error(w, "invalid mac address format (expected 00:1A:79:12:34:56)", http.StatusBadRequest)
			return
		}
		// Update existing profile if edit_id was provided
		if editIDStr != "" {
			id := atoiSafe(editIDStr)
			// stop running services (if any) before updating
			_ = StopRunner(id)
			SetProfileStopped(id)
			updated := false
			profMu.Lock()
			for i := range profiles {
				if profiles[i].ID == id {
					profiles[i].Name = name
					profiles[i].PortalURL = portal
					profiles[i].MAC = mac
					profiles[i].HlsPort = hlsPort
					profiles[i].ProxyPort = proxyPort
					profiles[i].Model = model
					profiles[i].SerialNumber = serialNumber
					profiles[i].DeviceID = deviceID
					profiles[i].DeviceID2 = deviceID2
					profiles[i].Signature = signature
					profiles[i].TimeZone = timezone
					profiles[i].Username = username
					profiles[i].Password = password
					profiles[i].WatchDogTime = watchdog
					updated = true
					break
				}
			}
			profMu.Unlock()
			if !updated {
				http.Error(w, "profile not found", http.StatusNotFound)
				return
			}
			_ = SaveProfiles()
			p, _ := GetProfile(id)
			go StartProfileServices(p)
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}

		p := AddProfile(Profile{
			Name:         name,
			PortalURL:    portal,
			MAC:          mac,
			HlsPort:      hlsPort,
			ProxyPort:    proxyPort,
			Model:        model,
			SerialNumber: serialNumber,
			DeviceID:     deviceID,
			DeviceID2:    deviceID2,
			Signature:    signature,
			TimeZone:     timezone,
			Username:     username,
			Password:     password,
			WatchDogTime: watchdog,
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
		host := externalHost(r)
		data := struct {
			Host     string
			Settings RuntimeSettings
			Profiles []Profile
		}{Host: host, Settings: GetRuntimeSettings(), Profiles: ListProfiles()}

		const tpl = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="https://i.ibb.co/MyxmyVzz/STALKERHEK-LOGO-1500x1500.png">
  <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.5.1/css/all.min.css" referrerpolicy="no-referrer" />
  <title>Stalkerhek Dashboard</title>
  <style>
    :root{--bg:#0a0f0a;--panel:#0d1410;--panel2:#111815;--border:#1f2e23;--text:#e0e6e0;--muted:#9aaa9a;--brand:#2d7a4e;--brand-hover:#3a8f5e;--ok:#3fb970;--warn:#d4a94a;--bad:#e85d4d}
    *{box-sizing:border-box}
    body{margin:0;font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Helvetica,Arial,sans-serif;background:linear-gradient(180deg, #0d1410 0%, #0a0f0a 100%);color:var(--text);min-height:100dvh}
    a{color:var(--brand);text-decoration:none} a:hover{color:var(--brand-hover);text-decoration:underline}
    .wrap{max-width:1200px;margin:0 auto;
      padding-top:calc(clamp(22px, 4.2vw, 40px) + env(safe-area-inset-top));
      padding-left:calc(clamp(20px, 4vw, 36px) + env(safe-area-inset-left));
      padding-right:calc(clamp(20px, 4vw, 36px) + env(safe-area-inset-right));
      padding-bottom:calc(130px + env(safe-area-inset-bottom));
      min-height:100dvh;display:flex;flex-direction:column;gap:14px}
    .topbar{display:flex;align-items:center;justify-content:center;gap:12px;flex-wrap:wrap;margin-bottom:6px}
    .banner{width:100%;max-width:1200px;border-radius:18px;border:1px solid var(--border);background:rgba(13,20,16,.55);box-shadow:0 18px 48px rgba(0,0,0,.42);overflow:hidden;height:clamp(108px, 17.5vw, 220px)}
    .banner img{width:100%;height:100%;display:block;object-fit:cover;object-position:center}
    .title{display:none}
    h1{margin:0;font-size:28px;letter-spacing:.1px;color:var(--text)}
    .sub{color:#c4d4c4;font-size:15px;line-height:1.45}
    .pill{display:inline-flex;align-items:center;gap:10px;padding:10px 14px;border:1px solid var(--border);border-radius:999px;background:rgba(31,46,35,.55);color:var(--muted);font-size:13px}
    .bottompills{position:fixed;left:50%;bottom:16px;transform:translateX(-50%);z-index:10;max-width:calc(1200px - 32px);width:calc(100% - 32px);display:flex;justify-content:center;pointer-events:none}
    .bottompills .pillrow{pointer-events:auto;display:flex;gap:10px;flex-wrap:wrap;justify-content:center}
    .bottompills .pill{background:rgba(13,20,16,.88);backdrop-filter:blur(10px);box-shadow:0 14px 40px rgba(0,0,0,.38)}
    a.pilllink{color:var(--muted);text-decoration:none}
    a.pilllink:hover{color:var(--text);text-decoration:none;border-color:rgba(45,122,78,.55);background:rgba(13,20,16,.92)}
    .grid{display:grid;grid-template-columns:1fr;gap:16px;flex:1 1 auto}
    @media(min-width:900px){.grid{grid-template-columns: 1fr}}
    .card{background:linear-gradient(180deg, rgba(17,24,21,.96), rgba(13,20,16,.94));border:1px solid var(--border);border-radius:18px;padding:24px;box-shadow:0 12px 32px rgba(0,0,0,.4)}
    .card h2{margin:0 0 12px 0;font-size:18px;color:var(--text)}
    .step{display:flex;gap:12px;align-items:flex-start;margin:12px 0}
    .num{display:none}
    .step p{margin:0;color:var(--muted);font-size:14px;line-height:1.4}
    label{display:block;font-size:13px;color:#c5d1c5;margin:12px 0 6px}
    .hint{font-size:13px;color:var(--muted);margin-top:8px;line-height:1.4}
    .row{display:grid;grid-template-columns:1fr;gap:12px}
    @media(min-width:520px){.row.two{grid-template-columns:1fr 1fr}}
    input{width:100%;padding:14px 14px;border-radius:12px;border:1px solid var(--border);background:#0f1612;color:var(--text);outline:none;font-size:16px;transition:border-color .2s,box-shadow .2s}
    input:focus{border-color:var(--brand);box-shadow:0 0 0 3px rgba(45,122,78,.2)}
    .err{display:none;margin-top:6px;color:var(--bad);font-size:12px}
    .btnbar{display:flex;gap:12px;flex-wrap:wrap;margin-top:16px}
    button{cursor:pointer;border:1px solid var(--border);border-radius:12px;padding:14px 16px;font-size:15px;font-weight:650;transition:background .2s,filter .2s, transform .18s ease, border-color .2s ease;display:inline-flex;align-items:center;gap:10px;background:rgba(13,20,16,.35);color:var(--text)}
    button:active{transform:translateY(1px)}
    button:focus-visible, a.ghost:focus-visible, a.ok:focus-visible, a.edit:focus-visible, a.danger:focus-visible{outline:none;box-shadow:0 0 0 3px rgba(45,122,78,.22)}
    .primary:hover{background:rgba(45,122,78,.16);border-color:rgba(45,122,78,.65)}
    .ghost{background:rgba(13,20,16,.25)}
    .ghost:hover{background:rgba(45,122,78,.12);border-color:rgba(45,122,78,.55)}
    a.ghost{display:inline-flex;align-items:center;justify-content:center;padding:12px 14px;border-radius:12px;font-size:13px;font-weight:700;gap:10px}
    .danger:hover{background:rgba(232,93,77,.14);border-color:rgba(232,93,77,.45)}
    .ok:hover{background:rgba(63,185,112,.14);border-color:rgba(63,185,112,.35)}
    .edit:hover{background:rgba(212,169,74,.12);border-color:rgba(212,169,74,.38)}
    .profiles{display:grid;grid-template-columns:1fr;gap:14px}
    .p{padding:16px;border-radius:16px;border:1px solid var(--border);background:rgba(13,20,16,.82);transition:transform .18s ease,border-color .18s ease,box-shadow .18s ease}
    .p:hover{transform:translateY(-1px);border-color:rgba(45,122,78,.55);box-shadow:0 14px 30px rgba(0,0,0,.25)}
    .phead{display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap;align-items:center}
    .pname{font-weight:800;color:var(--text)}
    .badg{font-size:12px;padding:6px 10px;border-radius:999px;border:1px solid var(--border);color:var(--muted)}
    .badg.ok{border-color:rgba(63,185,112,.3);color:#bfffd3}
    .badg.err{border-color:rgba(232,93,77,.35);color:#ffd0d0}
    .badg.run{border-color:rgba(45,122,78,.35);color:#d7e6ff}
    .meta{margin-top:12px;color:var(--muted);font-size:13px;line-height:1.4;display:grid;gap:6px}
    .links{display:none}
    .actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:14px;align-items:center}
    .actions .spacer{flex:1 1 auto}
    .linkbtn{display:inline-flex;align-items:center;gap:10px;text-decoration:none}
    .linkbtn:hover{text-decoration:none}
    .btntext{display:inline}

    @media (max-width: 560px){
      .wdesc{display:none}
      button{padding:11px 11px}
      a.ghost{padding:12px 12px}
      .btntext{display:none}
      .p{padding:14px}
      .actions{gap:8px}
      .proxybtn{display:none}
      .filtersbtn{display:none}
    }
    .footnote{margin-top:12px;color:var(--muted);font-size:12px;line-height:1.4}
    .toast{position:sticky;top:12px;z-index:20;max-width:1200px;margin:0 auto 12px auto;background:rgba(13,20,16,.92);border:1px solid var(--border);border-radius:14px;padding:12px 14px;display:none;box-shadow:0 16px 40px rgba(0,0,0,.35);backdrop-filter: blur(6px)}
    .toast strong{display:block;margin-bottom:4px}
    .toast .small{color:var(--muted);font-size:12px;margin-top:2px}

    /* Wizard stepper */
    [x-cloak]{display:none !important}
    .wizard{display:flex;gap:12px;flex-wrap:wrap;align-items:center;justify-content:center;margin:12px 0 18px 0}
    .wstep{display:flex;align-items:center;gap:10px;padding:14px 18px;border-radius:16px;border:1px solid var(--border);background:rgba(13,20,16,.55);color:#cfe0cf;cursor:pointer;user-select:none;transition:transform .18s ease, border-color .18s ease, background .18s ease;font-size:16px}
    .wstep:hover{transform:translateY(-1px);border-color:rgba(45,122,78,.6);background:rgba(13,20,16,.75)}
    .wstep.active{border-color:rgba(45,122,78,.75);box-shadow:0 0 0 3px rgba(45,122,78,.16) inset}
    .wbadge{display:none}
    .wmeta{display:flex;flex-direction:column;gap:1px}
    .wtitle{font-weight:800;color:#e6f2e6}
    .wdesc{font-size:12px;color:var(--muted)}

    /* Panels + subtle pattern backgrounds */
    .pane{position:relative;overflow:hidden}
    .pane::before{content:"";position:absolute;inset:0;pointer-events:none;opacity:.38}
    .pane.create::before{
      background-image:
        linear-gradient(to right, rgba(255,255,255,.035) 1px, transparent 1px),
        linear-gradient(to bottom, rgba(255,255,255,.03) 1px, transparent 1px);
      background-size: 44px 44px;
    }
    .pane.manage::before{
      background-image:
        radial-gradient(circle, rgba(255,255,255,.07) 1.7px, transparent 1.9px);
      background-size: 16px 16px;
      opacity:.55;
    }
	.pane.advanced::before{
	  background-image:
		repeating-linear-gradient(135deg, rgba(255,255,255,.05) 0px, rgba(255,255,255,.05) 1px, transparent 1px, transparent 12px),
		repeating-linear-gradient(45deg, rgba(255,255,255,.035) 0px, rgba(255,255,255,.035) 1px, transparent 1px, transparent 14px);
	  background-size: 180px 180px;
	  opacity:.28;
	}
    .pane > *{position:relative}

    .fade{transition:opacity .18s ease, transform .18s ease}
    .fadeIn{opacity:1;transform:none}
    .fadeOut{opacity:0;transform:translateY(6px)}

    .logbox{border:1px solid var(--border);border-radius:14px;background:rgba(15,22,18,.7);padding:12px;min-height:180px;max-height:360px;overflow:auto;scrollbar-gutter:stable}
    .logline{font-family:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,"Liberation Mono","Courier New",monospace;font-size:12.5px;color:#d6e4d6;line-height:1.45;padding:6px 8px;border-bottom:1px dashed rgba(31,46,35,.65)}
    .logline:last-child{border-bottom:none}
    .kpi{display:grid;grid-template-columns:repeat(auto-fit, minmax(180px, 1fr));gap:10px;margin-top:12px}
    .kpi .stat{background:rgba(31,46,35,.25);border:1px solid var(--border);border-radius:12px;padding:10px 12px}
    .kpi .stat .lbl{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.5px}
    .kpi .stat .val{color:#e6f2e6;font-weight:850;margin-top:4px;font-size:18px}

    /* Advanced pane layout */
    .advgrid{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:12px;margin-top:12px;max-width:980px;margin-left:auto;margin-right:auto}
    .advgrid .stat{background:rgba(31,46,35,.18);border:1px solid var(--border);border-radius:12px;padding:12px}
    .advgrid .stat .lbl{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.5px}
    .advgrid .stat input{margin-top:8px}
    .advactions{display:flex;justify-content:center;margin-top:14px}
  </style>
</head>
<body x-data="wizard()" x-init="init()" x-cloak>
  <!-- Alpine.js (CDN). Keeping it minimal avoids a Node build pipeline and stays stable for Go projects. -->
  <script defer src="https://unpkg.com/alpinejs@3/dist/cdn.min.js"></script>

  <div class="wrap">
    <div id="toast" class="toast"><strong id="toastTitle"></strong><div id="toastMsg"></div><div class="small" id="toastSmall"></div></div>

    <div class="topbar">
      <div class="banner" aria-label="Stalkerhek">
        <img src="https://i.ibb.co/WWL37xW9/STALKERHEK-BANNER-v2-3840x2160.png" alt="Stalkerhek" />
      </div>
    </div>

    <!-- Wizard stepper (reactive single-page flow) -->
    <div class="wizard" role="navigation" aria-label="Wizard">
      <div class="wstep" :class="step==='create' ? 'active' : ''" @click="go('create')" title="Create or edit profiles">
        <i class="fa-solid fa-plus"></i>
        <div class="wmeta"><div class="wtitle">Create</div><div class="wdesc">Add / Edit credentials</div></div>
      </div>
      <div class="wstep" :class="step==='manage' ? 'active' : ''" @click="go('manage')" title="Manage profiles">
		<i class="fa-solid fa-layer-group"></i>
        <div class="wmeta"><div class="wtitle">Manage</div><div class="wdesc">Start / Stop / Links</div></div>
      </div>
	  <div class="wstep" :class="step==='advanced' ? 'active' : ''" @click="go('advanced')" title="Advanced settings">
		<i class="fa-solid fa-sliders"></i>
		<div class="wmeta"><div class="wtitle">Advanced</div><div class="wdesc">Stability / Tuning</div></div>
	  </div>
    </div>

    <div class="grid">
      <!-- Create step -->
      <div class="card pane create" x-show="step==='create'" x-transition.opacity.duration.180ms>
        <h2>Create / Edit Profile</h2>
        <div class="step"><div class="num">1</div><p><b>Portal URL</b>: paste what your provider gave you. We'll fix it automatically if needed.</p></div>
        <div class="step"><div class="num">2</div><p><b>MAC</b>: copy/paste your MAC address (uppercase with colons).</p></div>
        <div class="step"><div class="num">3</div><p><b>Ports</b>: choose free ports (different for each profile).</p></div>

        <!--
          Developer note:
          This form supports both Create and Edit. When edit_id is set (by the Edit button), the server updates that profile,
          stops old services, and restarts with the new credentials.
        -->
        <form id="addForm" method="post" action="/profiles" novalidate @submit="onCreateSubmit()">
          <input type="hidden" id="edit_id" name="edit_id" value="" />
          <label for="name">Profile name (optional)</label>
          <input id="name" name="name" placeholder="Living Room / Office / Backup" title="Optional: give it a name so you can recognize it" />

          <label for="portal">Portal URL (required)</label>
          <input id="portal" name="portal" required placeholder="http://example.com/portal.php" title="Paste your portal URL here" />
          <div id="portalErr" class="err">Please paste a valid portal link. We'll try to fix it automatically.</div>

          <label for="mac">MAC address (required)</label>
          <input id="mac" name="mac" required placeholder="00:1A:79:12:34:56" title="Example format: 00:1A:79:12:34:56" />
          <div id="macErr" class="err">MAC must look like <b>00:1A:79:12:34:56</b>.</div>

          <div class="row two">
            <div>
              <label for="hls_port">HLS Port</label>
              <input id="hls_port" name="hls_port" required inputmode="numeric" title="This is the port your HLS players will use" />
            </div>
            <div>
              <label for="proxy_port">Proxy Port</label>
              <input id="proxy_port" name="proxy_port" required inputmode="numeric" title="This is used by STB-style apps (optional for most users)" />
            </div>
          </div>

          <details style="margin-top:12px">
            <summary style="cursor:pointer;color:#7fba7f;font-size:.95em;user-select:none">Advanced Settings (optional)</summary>
            <div style="margin-top:10px">
              <div class="row two">
                <div>
                  <label for="username">Username</label>
                  <input id="username" name="username" placeholder="Leave blank for Device ID auth" title="Portal username (if your provider uses login/password)" />
                </div>
                <div>
                  <label for="password">Password</label>
                  <input id="password" name="password" type="password" placeholder="Leave blank for Device ID auth" title="Portal password" />
                </div>
              </div>
              <div class="row two">
                <div>
                  <label for="model">Model</label>
                  <input id="model" name="model" placeholder="MAG254" title="STB model identifier" />
                </div>
                <div>
                  <label for="serial_number">Serial Number</label>
                  <input id="serial_number" name="serial_number" placeholder="0000000000000" title="STB serial number" />
                </div>
              </div>
              <label for="device_id">Device ID</label>
              <input id="device_id" name="device_id" placeholder="ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" title="64-char hex device ID" />
              <label for="device_id2">Device ID 2</label>
              <input id="device_id2" name="device_id2" placeholder="ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" title="64-char hex secondary device ID" />
              <label for="signature">Signature</label>
              <input id="signature" name="signature" placeholder="ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" title="64-char hex signature" />
              <div class="row two">
                <div>
                  <label for="timezone">Time Zone</label>
                  <input id="timezone" name="timezone" placeholder="UTC" title="IANA timezone (e.g. UTC, America/New_York)" />
                </div>
                <div>
                  <label for="watchdog_time">Watchdog Interval (min)</label>
                  <input id="watchdog_time" name="watchdog_time" inputmode="numeric" placeholder="5" title="Watchdog keep-alive interval in minutes" />
                </div>
              </div>
            </div>
          </details>

          <div class="btnbar">
            <button class="primary" id="saveBtn" type="submit" title="Saves profile to the list below"><i class="fa-regular fa-floppy-disk"></i> <span class="btntext">Save Profile</span></button>
            <button class="ghost" type="button" id="cancelEdit" style="display:none" title="Cancel editing and reset the form"><i class="fa-solid fa-xmark"></i> <span class="btntext">Cancel Edit</span></button>
          </div>
          <div class="hint" id="formHint">Tip: After saving, it will start automatically. When it's ready, you'll see the copy buttons in Manage.</div>
        </form>
      </div>

      <!-- Manage step -->
      <div class="card pane manage" x-show="step==='manage'" x-transition.opacity.duration.180ms>
      <h2>Manage Profiles</h2>
      <div class="sub">Start or stop streaming, copy your links, or update your details. Editing will stop the stream first for safety.</div>
      <div id="profiles" class="profiles">
        {{range .Profiles}}
          <div class="p" data-id="{{.ID}}" data-name="{{.Name}}" data-portal="{{.PortalURL}}" data-mac="{{.MAC}}" data-hls="{{.HlsPort}}" data-proxy="{{.ProxyPort}}" data-model="{{.Model}}" data-serial="{{.SerialNumber}}" data-deviceid="{{.DeviceID}}" data-deviceid2="{{.DeviceID2}}" data-signature="{{.Signature}}" data-timezone="{{.TimeZone}}" data-username="{{.Username}}" data-password="{{.Password}}" data-watchdog="{{.WatchDogTime}}">
            <div class="phead">
              <div>
                <div class="pname">{{if .Name}}{{.Name}}{{else}}Profile {{.ID}}{{end}}</div>
                <div class="sub" style="margin-top:6px">Portal: <span style="color:#c5d1c5">{{.PortalURL}}</span></div>
                <div class="sub">MAC: <span style="color:#c5d1c5">{{.MAC}}</span></div>
              </div>
              <div class="badg" id="badge-{{.ID}}" title="Current status of this profile">Idle</div>
            </div>

            <div class="actions">
              <form method="post" action="#" style="margin:0" onsubmit="return false" title="Starts this profile (authenticates, fetches channels, and launches HLS/Proxy)">
                <input type="hidden" name="id" value="{{.ID}}" />
                <button class="ok" id="startbtn-{{.ID}}" type="button" @click="onStartClicked({{.ID}})" title="Start this profile"><i class="fa-solid fa-play"></i> <span class="btntext">Start</span></button>
              </form>
              <form method="post" action="#" style="margin:0" onsubmit="return false" title="Stops streaming for this profile">
                <input type="hidden" name="id" value="{{.ID}}" />
                <button class="ghost" id="stopbtn-{{.ID}}" type="submit" title="Stop this profile"><i class="fa-solid fa-stop"></i> <span class="btntext">Stop</span></button>
              </form>
              <form method="post" action="#" style="margin:0" onsubmit="return false" title="Edit this profile (fills the form above)">
                <button class="edit" type="button" data-action="edit" title="Edit this profile"><i class="fa-solid fa-pen"></i> <span class="btntext">Edit</span></button>
              </form>
              <form method="post" action="/profiles/delete" style="margin:0" onsubmit="return confirm('Delete this profile? This cannot be undone.')" title="Removes this profile from the list">
                <input type="hidden" name="id" value="{{.ID}}" />
                <button class="danger" type="submit" title="Delete this profile"><i class="fa-solid fa-trash"></i> <span class="btntext">Delete</span></button>
              </form>
              <div class="spacer"></div>
              <a class="ghost linkbtn" id="hls-{{.ID}}" href="#" data-copy="http://{{$.Host}}:{{.HlsPort}}/" title="Copy HLS endpoint"><i class="fa-solid fa-film"></i> <span class="btntext">HLS</span></a>
              <a class="ghost linkbtn proxybtn" id="pxy-{{.ID}}" href="#" data-copy="http://{{$.Host}}:{{.ProxyPort}}/" title="Copy Proxy endpoint"><i class="fa-solid fa-right-left"></i> <span class="btntext">Proxy</span></a>
              <a class="ghost linkbtn" href="/filters?id={{.ID}}" target="_blank" rel="noopener" title="Filter channels/genres for this profile"><i class="fa-solid fa-filter"></i> <span class="btntext">Filters</span></a>
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

	  <!-- Advanced step -->
	  <div class="card pane advanced" x-show="step==='advanced'" x-transition.opacity.duration.180ms>
		<h2>Advanced Settings</h2>
		<div class="sub">Optional tuning for stability and proxies. These apply immediately and are process-local.</div>
		<form id="settingsForm" onsubmit="return false">
		  <div class="advgrid">
			<div class="stat">
			  <div class="lbl">Playlist delay (segments)</div>
			  <input id="s_delay" name="playlist_delay_segments" inputmode="numeric" placeholder="Example: 10" title="Helps prevent random buffering by playing a little behind live.&#10;&#10;Good starting point:&#10;- 10&#10;If it still buffers:&#10;- 15 to 20" />
			  <div class="hint">Adds latency but can reduce buffering.</div>
			</div>
			<div class="stat">
			  <div class="lbl">Upstream header timeout (sec)</div>
			  <input id="s_rht" name="response_header_timeout_seconds" inputmode="numeric" placeholder="Example: 15" title="How long we wait for your provider to start responding.&#10;&#10;Recommended:&#10;- 15 (default)&#10;- 20 to 30 if your provider is slow" />
			  <div class="hint">How long to wait for upstream response headers.</div>
			</div>
			<div class="stat">
			  <div class="lbl">Max idle conns/host</div>
			  <input id="s_idle" name="max_idle_conns_per_host" inputmode="numeric" placeholder="Example: 64" title="How many spare connections we keep ready.&#10;&#10;Recommended:&#10;- 64 (default)&#10;- 96 to 128 if many devices stream at once" />
			  <div class="hint">Higher can improve concurrency.</div>
			</div>
		  </div>
		  <div class="advactions">
			<button class="primary" type="button" id="saveSettings"><i class="fa-regular fa-floppy-disk"></i> <span class="btntext">Save Settings</span></button>
		  </div>
		</form>
		<div class="footnote">Tip: Leave a box empty if you don't want to change that setting.</div>
	  </div>
  </div>

  <script>
    //
    // Wizard controller (Alpine.js)
    //
    // Goals:
    // - Keep this page single-file and Go-served (no Node build).
    // - Provide a clean 2-step experience (Create -> Manage).
    // - Reuse existing /api/profile_status polling for live updates.
    //
    function wizard(){
      return {
        step: 'create',
        activeID: '',
        activeName: '',
        init(){
          // Default to manage if there are existing profiles.
          try{
            const hasProfiles = document.querySelectorAll('#profiles .p[data-id]').length > 0;
            if(hasProfiles) this.step = 'manage';
          }catch(e){}
        },
        go(s){ this.step = s; },
        setActiveFromCard(card){
          if(!card) return;
          this.activeID = card.getAttribute('data-id')||'';
          this.activeName = card.getAttribute('data-name') || ('Profile '+this.activeID);
        },
        onStartClicked(id){
          this.activeID = String(id||'');
		  this.activeName = 'Profile ' + this.activeID;
          this.step = 'manage';
          try{ postForm('/api/profiles/start', {id: this.activeID}); }catch(e){}
        },
		onStopClicked(id){
		  const pid = String(id||'');
		  if(!pid) return;
		  try{ postForm('/api/profiles/stop', {id: pid}); }catch(e){}
		  showToast('Stopped', 'Profile stopped.');
		},
        onCreateSubmit(){
          // When user saves/updates a profile, keep the UI simple.
          this.step = 'manage';
        }
      }
    }

    const macRe = /^[0-9A-F]{2}(:[0-9A-F]{2}){5}$/;
    function normalizePortal(raw){
      let s = (raw||'').trim();
      if(!s) return '';
      if(!/^https?:\/\//i.test(s)) s = 'http://' + s;
      try{
        const u = new URL(s);
        let p = (u.pathname||'/').trim();
        if(!p || p === '/') p = '/portal.php';
        if(!/\/portal\.php$/i.test(p) && !/\/load\.php$/i.test(p)){
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
      const small=document.getElementById('toastSmall');
      if(small) small.textContent='';
      t.style.display='block';
      clearTimeout(window.__toastTimer);
      window.__toastTimer=setTimeout(()=>t.style.display='none', 3800);
    }

    // Developer note: use form-encoded bodies for maximum compatibility with Go's r.FormValue().
    async function postForm(url, data){
      const body = Object.entries(data).map(([k,v]) => encodeURIComponent(k)+'='+encodeURIComponent(String(v))).join('&');
      return fetch(url, {method:'POST', headers:{'Content-Type':'application/x-www-form-urlencoded'}, body});
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
      const portalOk = /^https?:\/\//i.test(v) && /(portal|load)\.php(\?.*)?$/i.test(v);
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
    function copyText(text){
      if(!text) return Promise.reject(new Error('empty'));
      if(navigator.clipboard && navigator.clipboard.writeText){
        return navigator.clipboard.writeText(text);
      }
      return new Promise((resolve, reject)=>{
        try{
          const ta=document.createElement('textarea');
          ta.value=text;
          ta.setAttribute('readonly','');
          ta.style.position='fixed';
          ta.style.top='-1000px';
          document.body.appendChild(ta);
          ta.select();
          const ok=document.execCommand('copy');
          document.body.removeChild(ta);
          if(ok) resolve(); else reject(new Error('copy failed'));
        }catch(e){ reject(e); }
      });
    }

    document.getElementById('profiles').addEventListener('click', (e)=>{
      const a = e.target && e.target.closest ? e.target.closest('a[data-copy]') : null;
      if(!a) return;
      e.preventDefault();
      const url = a.getAttribute('data-copy') || '';
      copyText(url).then(()=>{
        showToast('Copied', url);
      }).catch(()=>{
        showToast('Copy failed', 'Your browser blocked clipboard access.');
      });
    });

    function resetEdit(){
      document.getElementById('edit_id').value='';
      document.getElementById('saveBtn').innerHTML='<i class="fa-regular fa-floppy-disk"></i> <span class="btntext">Save Profile</span>';
      document.getElementById('cancelEdit').style.display='none';
      const hint=document.getElementById('formHint');
      if(hint) hint.textContent='Tip: After saving, the profile will start automatically. Links will appear below once ready.';
    }
    document.getElementById('cancelEdit').addEventListener('click', ()=>{
      resetEdit();
      document.getElementById('addForm').reset();
      showToast('Edit canceled', 'Form reset back to create mode.');
    });

    document.getElementById('profiles').addEventListener('click', (e)=>{
      const btn = e.target && e.target.closest ? e.target.closest('button[data-action="edit"]') : null;
      if(!btn) return;
      const card = btn.closest('.p');
      if(!card) return;
      const id = card.getAttribute('data-id')||'';
      const name = card.getAttribute('data-name')||'';
      const portal = card.getAttribute('data-portal')||'';
      const mac = card.getAttribute('data-mac')||'';
      const hls = card.getAttribute('data-hls')||'';
      const proxy = card.getAttribute('data-proxy')||'';

	  // Stop running services first to ensure safe credential update
	  fetch('/api/profiles/stop', {
		method: 'POST',
		headers: {'Content-Type':'application/x-www-form-urlencoded'},
		body: 'id=' + encodeURIComponent(id)
	  }).catch(()=>{});

      document.getElementById('edit_id').value=id;
      document.getElementById('name').value=name;
      document.getElementById('portal').value=portal;
      document.getElementById('mac').value=mac;
      document.getElementById('hls_port').value=hls;
      document.getElementById('proxy_port').value=proxy;
      document.getElementById('model').value=card.getAttribute('data-model')||'';
      document.getElementById('serial_number').value=card.getAttribute('data-serial')||'';
      document.getElementById('device_id').value=card.getAttribute('data-deviceid')||'';
      document.getElementById('device_id2').value=card.getAttribute('data-deviceid2')||'';
      document.getElementById('signature').value=card.getAttribute('data-signature')||'';
      document.getElementById('timezone').value=card.getAttribute('data-timezone')||'';
      document.getElementById('username').value=card.getAttribute('data-username')||'';
      document.getElementById('password').value=card.getAttribute('data-password')||'';
      document.getElementById('watchdog_time').value=card.getAttribute('data-watchdog')||'';

      document.getElementById('saveBtn').innerHTML='<i class="fa-regular fa-floppy-disk"></i> <span class="btntext">Save Changes</span>';
      document.getElementById('cancelEdit').style.display='inline-flex';
      const hint=document.getElementById('formHint');
      if(hint) hint.textContent='Editing will update this profile, stop any running services, then restart automatically.';
	  showToast('Editing profile', 'Stopped the running playlist for safety. Make changes and click Save Changes to apply.');
      Alpine.$data(document.querySelector('[x-data]')).step='create';
      window.scrollTo({top:0, behavior:'smooth'});
    });

    async function poll(){
      try{
        const r = await fetch('/api/profile_status', {cache:'no-store'});
        const a = await r.json();
        for(const s of a){
          const badge=document.getElementById('badge-'+s.id);
          const meta=document.getElementById('meta-'+s.id);
          const startBtn=document.getElementById('startbtn-'+s.id);
          if(!badge || !meta) continue;
          badge.className='badg';
          if(s.phase==='success') badge.classList.add('ok');
          if(s.phase==='error') badge.classList.add('err');
          if(s.running) badge.classList.add('run');
          const label = s.busy ? 'Starting…' : (s.running ? 'Running' : (s.phase==='success' ? (s.message||'Ready') : (s.phase==='error' ? 'Error' : (s.phase==='validating' ? 'Working…' : 'Idle'))));
          badge.textContent = label;
          let lines=[];
          if(s.message) lines.push(s.message);
          if(s.phase==='error') lines.push('Tip: open Instance Logs for details.');
          if(s.channels) lines.push('Channels: '+s.channels);
          if(s.busy) lines.push('Starting in progress…');
          if(lines.length===0) lines.push('');
          meta.innerHTML = '<div>'+lines.map(x=>String(x).replace(/</g,'&lt;')).join('</div><div>')+'</div>';
          if(startBtn){
            const disabled = !!s.busy || !!s.running;
            startBtn.disabled = disabled;
            startBtn.title = s.busy ? 'Already starting… please wait' : (s.running ? 'Already running' : 'Start this profile');
            			startBtn.style.opacity = disabled ? '0.65' : '';
			startBtn.style.cursor = disabled ? 'not-allowed' : '';
		  }
		}
      }catch(e){}
    }
    setInterval(poll, 1200);
    poll();

	// Save advanced settings
	document.getElementById('saveSettings').addEventListener('click', async ()=>{
		const delay=document.getElementById('s_delay').value||'';
		const rht=document.getElementById('s_rht').value||'';
		const idle=document.getElementById('s_idle').value||'';
		try{
			await postForm('/api/settings', {
				playlist_delay_segments: delay,
				response_header_timeout_seconds: rht,
				max_idle_conns_per_host: idle,
			});
			showToast('Saved', 'Settings applied.');
		}catch(e){
			showToast('Save failed', 'Could not save settings.');
		}
	});
  </script>

  <div class="bottompills">
    <div class="pillrow">
      <div class="pill" title="This is the address your devices should use">
        <i class="fa-solid fa-network-wired"></i> Host: <b>{{.Host}}</b>
      </div>
      <a class="pill pilllink" href="/logs" target="_blank" rel="noopener" title="Open live logs (helps with troubleshooting)">
        <i class="fa-regular fa-file-lines"></i> Logs
      </a>
      <a class="pill pilllink" href="https://github.com/kidpoleon/stalkerhek" target="_blank" rel="noopener" title="View source and report issues">
        <i class="fa-brands fa-github"></i> GitHub
      </a>
    </div>
  </div>
</body>
</html>`

		t := template.Must(template.New("dash").Parse(tpl))
		_ = t.Execute(w, data)
	})

	mux.HandleFunc("/filters", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		ua := strings.ToLower(strings.TrimSpace(r.Header.Get("User-Agent")))
		if strings.Contains(ua, "mobile") || strings.Contains(ua, "android") || strings.Contains(ua, "iphone") || strings.Contains(ua, "ipad") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Filters - Desktop Required</title><style>:root{--bg:#0a0f0a;--panel:#0d1410;--border:#1f2e23;--text:#e0e6e0;--muted:#9aaa9a;--brand:#2d7a4e}*{box-sizing:border-box}body{margin:0;font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Helvetica,Arial,sans-serif;background:linear-gradient(180deg,#0d1410 0%,#0a0f0a 100%);color:var(--text);min-height:100dvh;display:flex;align-items:center;justify-content:center;padding:18px}a{color:var(--brand)}.card{max-width:520px;width:100%;border:1px solid var(--border);border-radius:18px;background:rgba(13,20,16,.75);padding:18px;box-shadow:0 18px 48px rgba(0,0,0,.42)}h1{margin:0 0 8px 0;font-size:18px}.sub{color:var(--muted);line-height:1.5}</style></head><body><div class="card"><h1>Filters require a desktop browser</h1><div class="sub">Channel filtering is a power feature and is intentionally desktop-only for clarity and safety.<br><br>Please open this page on a desktop/laptop browser.</div><div class="sub" style="margin-top:12px"><a href="/dashboard">Back to Dashboard</a></div></div></body></html>`))
			return
		}
		idStr := strings.TrimSpace(r.URL.Query().Get("id"))
		pid := atoiSafe(idStr)
		data := struct {
			ProfileID int
			Profiles  []Profile
		}{ProfileID: pid, Profiles: ListProfiles()}

		const tpl = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="https://i.ibb.co/MyxmyVzz/STALKERHEK-LOGO-1500x1500.png">
  <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.5.1/css/all.min.css" referrerpolicy="no-referrer" />
  <title>Stalkerhek Filters</title>
  <style>
    :root{--bg:#0a0f0a;--panel:#0d1410;--panel2:#111815;--border:#1f2e23;--text:#e0e6e0;--muted:#9aaa9a;--brand:#2d7a4e;--brand-hover:#3a8f5e;--ok:#3fb970;--warn:#d4a94a;--bad:#e85d4d}
    *{box-sizing:border-box}
    body{margin:0;font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Helvetica,Arial,sans-serif;background:linear-gradient(180deg, #0d1410 0%, #0a0f0a 100%);color:var(--text);min-height:100dvh}
    a{color:var(--brand);text-decoration:none} a:hover{color:var(--brand-hover);text-decoration:underline}
    .wrap{max-width:1200px;margin:0 auto;
      padding-top:calc(clamp(22px, 4.2vw, 36px) + env(safe-area-inset-top));
      padding-left:calc(clamp(20px, 4vw, 32px) + env(safe-area-inset-left));
      padding-right:calc(clamp(20px, 4vw, 32px) + env(safe-area-inset-right));
      padding-bottom:calc(40px + env(safe-area-inset-bottom));
      display:flex;flex-direction:column;gap:14px}
    .card{background:linear-gradient(180deg, rgba(17,24,21,.96), rgba(13,20,16,.94));border:1px solid var(--border);border-radius:18px;padding:18px;box-shadow:0 12px 32px rgba(0,0,0,.4)}
    h1{margin:0 0 6px 0;font-size:20px}
    .sub{color:#c4d4c4;font-size:13px;line-height:1.45}
    .row{display:flex;gap:12px;flex-wrap:wrap;align-items:center}
    .row.tight{gap:10px}
    select,input{background:#0f1612;border:1px solid var(--border);border-radius:12px;padding:12px 12px;color:var(--text);outline:none;font-size:14px}
    input:focus,select:focus{border-color:var(--brand);box-shadow:0 0 0 3px rgba(45,122,78,.2)}
    button{cursor:pointer;border:1px solid var(--border);border-radius:12px;padding:11px 12px;font-size:13px;font-weight:700;transition:background .2s,transform .18s ease,border-color .2s ease;display:inline-flex;align-items:center;gap:10px;background:rgba(13,20,16,.35);color:var(--text)}
    button:active{transform:translateY(1px)}
    button.primary:hover{background:rgba(45,122,78,.16);border-color:rgba(45,122,78,.65)}
    button.ghost:hover{background:rgba(45,122,78,.12);border-color:rgba(45,122,78,.55)}
    button.danger:hover{background:rgba(232,93,77,.14);border-color:rgba(232,93,77,.45)}
    .grid{display:grid;grid-template-columns:1fr;gap:14px;align-items:start}
    @media(min-width:980px){.grid{grid-template-columns: minmax(280px,360px) minmax(0,1fr)}}
    .list{display:grid;gap:8px}
    .item{border:1px solid var(--border);border-radius:14px;background:rgba(13,20,16,.76);padding:10px 12px;display:flex;justify-content:space-between;gap:10px;align-items:center;min-width:0}
    .item .name{font-weight:800}
    .pill{display:inline-flex;align-items:center;gap:8px;padding:6px 10px;border:1px solid var(--border);border-radius:999px;color:var(--muted);font-size:12px}
    .pill.ok{border-color:rgba(63,185,112,.3);color:#bfffd3}
    .pill.bad{border-color:rgba(232,93,77,.35);color:#ffd0d0}
    .pill.mix{border-color:rgba(212,169,74,.45);color:#ffe0aa}
    .mono{font-family:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,"Liberation Mono","Courier New",monospace}
    .small{font-size:12px;color:var(--muted)}
    .table{border:1px solid var(--border);border-radius:16px;overflow:hidden}
    .tableWrap{overflow:auto;max-width:100%}
    .thead,.trow{display:grid;grid-template-columns: minmax(260px,1fr) minmax(160px,220px) minmax(140px,160px);gap:10px;align-items:center}
    .thead{background:rgba(31,46,35,.25);padding:10px 12px;color:#cfe0cf;font-size:12px;text-transform:uppercase;letter-spacing:.6px}
    .trow{padding:10px 12px;border-top:1px solid rgba(31,46,35,.55);cursor:pointer}
    .trow:hover{background:rgba(45,122,78,.08)}
    .trow.active{background:rgba(45,122,78,.12)}
    .toggle{display:flex;gap:8px;justify-content:flex-end}
	.name{overflow-wrap:anywhere;word-break:break-word}
	.small{overflow-wrap:anywhere;word-break:break-word}
	.mono{overflow-wrap:anywhere;word-break:break-word}


	.grp{border:1px solid var(--border);border-radius:16px;background:rgba(13,20,16,.58);overflow:hidden}
	.ghead{display:flex;justify-content:space-between;align-items:center;gap:10px;padding:10px 12px;background:rgba(31,46,35,.18);cursor:pointer;user-select:none}
	.ghead .ttl{font-weight:900}
	.gbody{padding:10px;display:none}
	.grp.open .gbody{display:block}
	.gmeta{color:var(--muted);font-size:12px}

	.chips{display:flex;flex-wrap:wrap;gap:8px;margin-top:10px}
	.chip{display:inline-flex;gap:8px;align-items:center;padding:7px 10px;border-radius:999px;border:1px solid rgba(31,46,35,.7);background:rgba(13,20,16,.58);color:#cfe0cf;font-size:12px}
	.chip button{padding:6px 8px;border-radius:999px}

	.drawerBack{position:fixed;inset:0;background:rgba(0,0,0,.55);display:none;z-index:60}
	.drawer{position:fixed;top:0;right:0;height:100dvh;width:min(420px, 92vw);background:linear-gradient(180deg, rgba(17,24,21,.98), rgba(13,20,16,.98));border-left:1px solid var(--border);box-shadow:-18px 0 48px rgba(0,0,0,.45);display:none;z-index:61;padding:16px;overflow:auto}
	.drawer.open,.drawerBack.open{display:block}
	.drawer h2{margin:0 0 8px 0;font-size:16px}
	.kv{display:grid;gap:6px;margin-top:10px}
	.kv .k{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.6px}
	.kv .v{color:var(--text);font-size:13px;overflow-wrap:anywhere}
	.drawer .btnrow{display:flex;gap:10px;flex-wrap:wrap;margin-top:12px}
	/* themed checkbox */
	.ck{display:inline-flex;align-items:center;justify-content:center;width:20px;height:20px}
	.ck input{appearance:none;-webkit-appearance:none;width:18px;height:18px;border-radius:6px;border:1px solid rgba(31,46,35,.9);background:rgba(13,20,16,.55);box-shadow:inset 0 0 0 1px rgba(0,0,0,.35);cursor:pointer;display:inline-block;position:relative}
	.ck input:focus{outline:none;box-shadow:0 0 0 3px rgba(45,122,78,.22), inset 0 0 0 1px rgba(0,0,0,.35);border-color:rgba(45,122,78,.75)}
	.ck input:checked{background:rgba(45,122,78,.22);border-color:rgba(45,122,78,.85)}
	.ck input:checked::after{content:"";position:absolute;left:5px;top:1px;width:4px;height:9px;border:2px solid #bfffd3;border-top:0;border-left:0;transform:rotate(45deg)}

	.modalBack{position:fixed;inset:0;background:rgba(0,0,0,.62);display:none;z-index:70}
	.modal{position:fixed;left:50%;top:50%;transform:translate(-50%,-50%);width:min(540px, 92vw);border:1px solid var(--border);border-radius:18px;background:linear-gradient(180deg, rgba(17,24,21,.98), rgba(13,20,16,.98));box-shadow:0 24px 70px rgba(0,0,0,.55);padding:16px;display:none;z-index:71}
	.modal.open,.modalBack.open{display:block}
	.modal h3{margin:0 0 8px 0;font-size:16px}
	.modal .txt{color:#cfe0cf;font-size:13px;line-height:1.5}
	.modal .btnrow{display:flex;gap:10px;justify-content:flex-end;flex-wrap:wrap;margin-top:14px}
	.tip{position:fixed;left:0;top:0;max-width:min(520px, 92vw);background:rgba(13,20,16,.96);border:1px solid var(--border);border-radius:14px;box-shadow:0 16px 46px rgba(0,0,0,.5);padding:12px 12px;z-index:80;display:none;pointer-events:none}
	.tip strong{display:block;margin-bottom:6px}
	.tip .lines{display:grid;gap:4px}
	.tip .lines div{color:#cfe0cf;font-size:12px;overflow-wrap:anywhere}
	@media(max-width:780px){
	  .thead,.trow{grid-template-columns: minmax(260px,1fr) minmax(140px,200px) minmax(140px,160px)}
	}
	@media(max-width:520px){
	  .thead,.trow{min-width:720px}
	}
    .toast{position:sticky;top:12px;z-index:20;background:rgba(13,20,16,.92);border:1px solid var(--border);border-radius:14px;padding:12px 14px;display:none;box-shadow:0 16px 40px rgba(0,0,0,.35);backdrop-filter: blur(6px)}
    .toast strong{display:block;margin-bottom:4px}
  </style>
</head>
<body>
  <div class="wrap">
    <div id="toast" class="toast"><strong id="toastTitle"></strong><div id="toastMsg"></div><div class="small" id="toastSmall"></div></div>

	<div id="drawerBack" class="drawerBack" aria-hidden="true"></div>
	<div id="drawer" class="drawer" role="dialog" aria-modal="true" aria-labelledby="dTitle" aria-describedby="dSub" tabindex="-1">
	  <div class="row" style="justify-content:space-between;align-items:center">
		<div style="font-weight:900">Details</div>
		<button class="ghost" id="drawerClose" type="button"><i class="fa-solid fa-xmark"></i> Close</button>
	  </div>
	  <h2 id="dTitle"></h2>
	  <div class="small" id="dSub"></div>
	  <div class="kv">
		<div class="k">Genre</div><div class="v" id="dGenre"></div>
		<div class="k">CMD</div><div class="v mono" id="dCmd"></div>
		<div class="k">State</div><div class="v" id="dState"></div>
	  </div>
	  <div class="btnrow">
		<button class="primary" id="dToggle" type="button"></button>
		<button class="ghost" id="dCopy" type="button"><i class="fa-regular fa-copy"></i> Copy CMD</button>
	  </div>
	  <div class="small" style="margin-top:10px">Tip: use Search to find a channel by name, then click it for details.</div>
	</div>

	<div id="modalBack" class="modalBack" aria-hidden="true"></div>
	<div id="modal" class="modal" role="dialog" aria-modal="true" aria-labelledby="mTitle" aria-describedby="mBody" tabindex="-1">
	  <h3 id="mTitle"></h3>
	  <div class="txt" id="mBody"></div>
	  <div class="btnrow">
		<button class="ghost" id="mCancel" type="button">Cancel</button>
		<button class="danger" id="mConfirm" type="button">Confirm</button>
	  </div>
	</div>

	<div id="tip" class="tip" role="status" aria-live="polite" aria-atomic="true"><strong id="tipTitle"></strong><div class="lines" id="tipLines"></div></div>

    <div class="card">
      <h1><i class="fa-solid fa-filter"></i> Channel Filters</h1>
      <div class="sub">Fast, per-profile filtering. Changes apply immediately to playlist, streams, and proxy.</div>
      <div class="row tight" style="margin-top:12px">
        <div class="pill" title="Profile">Profile</div>
        <select id="profileSel" title="Choose a profile">
          {{range .Profiles}}
            <option value="{{.ID}}" {{if eq .ID $.ProfileID}}selected{{end}}>{{if .Name}}{{.Name}}{{else}}Profile {{.ID}}{{end}}</option>
          {{end}}
        </select>
        <button class="ghost" id="reloadBtn" type="button"><i class="fa-solid fa-rotate"></i> Reload</button>
        <div style="flex:1 1 auto"></div>
        <button class="danger" id="resetBtn" type="button" title="Clear all filters for this profile"><i class="fa-solid fa-eraser"></i> Reset</button>
      </div>
      <div id="chips" class="chips"></div>
      <div class="small" id="hint" style="margin-top:10px"></div>
    </div>

    <div class="card" id="errBanner" style="display:none;border-color:rgba(232,93,77,.45);background:rgba(232,93,77,.08)">
      <div style="font-weight:900">Something went wrong</div>
      <div class="small" id="errMsg"></div>
    </div>

    <div class="card">
      <div class="row" style="justify-content:space-between;align-items:flex-start">
        <div>
          <div style="font-weight:900">Filters Flow</div>
          <div class="small">Pick a Category, then a Genre, then fine-tune Channels. Bulk actions at every step.</div>
        </div>
        <div class="row">
          <div class="pill" id="crumb">Categories</div>
          <button class="ghost" id="backBtn" type="button" style="display:none"><i class="fa-solid fa-arrow-left"></i> Back</button>
        </div>
      </div>
    </div>

    <div class="grid" style="grid-template-columns:1fr">
      <div class="card" id="viewCategories">
        <div class="row" style="justify-content:space-between;align-items:flex-start">
          <div>
            <div style="font-weight:900">Categories</div>
            <div class="small">Derived from genre names (e.g. <span class="mono">MX| ...</span> becomes <span class="mono">MX</span>).</div>
          </div>
          <input id="catFilter" placeholder="Search categories" />
        </div>
        		<div class="row" style="margin-top:10px">
		  <button class="ghost" id="catSelAll" type="button"><i class="fa-regular fa-square-check"></i> Select All</button>
		  <button class="ghost" id="catSelNone" type="button"><i class="fa-regular fa-square"></i> Select None</button>
		  <button class="ghost" id="catEnable" type="button" disabled><i class="fa-solid fa-eye"></i> Enable Selected</button>
		  <button class="ghost" id="catDisable" type="button" disabled><i class="fa-solid fa-eye-slash"></i> Disable Selected</button>
		  <div class="pill" id="catSelCount" aria-live="polite" aria-atomic="true">0 selected</div>
		  <div class="small" id="catHint"></div>
		</div>
        <div class="list" id="cats" style="margin-top:12px"></div>
      </div>

      <div class="card" id="viewGenres" style="display:none">
        <div class="row" style="justify-content:space-between;align-items:flex-start">
          <div>
            <div style="font-weight:900">Genres</div>
            <div class="small">Within selected category.</div>
          </div>
          <input id="genreFilter" placeholder="Search genres" />
        </div>
        		<div class="row" style="margin-top:10px">
		  <button class="ghost" id="genreSelAll" type="button"><i class="fa-regular fa-square-check"></i> Select All</button>
		  <button class="ghost" id="genreSelNone" type="button"><i class="fa-regular fa-square"></i> Select None</button>
		  <button class="ghost" id="genreEnable" type="button" disabled><i class="fa-solid fa-eye"></i> Enable Selected</button>
		  <button class="ghost" id="genreDisable" type="button" disabled><i class="fa-solid fa-eye-slash"></i> Disable Selected</button>
		  <div class="pill" id="genreSelCount" aria-live="polite" aria-atomic="true">0 selected</div>
		  <div class="small" id="genreHint"></div>
		</div>
        <div class="list" id="genres" style="margin-top:12px"></div>
      </div>

      <div class="card" id="viewChannels" style="display:none">
        <div class="row" style="justify-content:space-between;align-items:flex-start">
          <div>
            <div style="font-weight:900">Channels</div>
            <div class="small">Within selected genre. Use bulk select for fast changes.</div>
          </div>
          <div class="row">
            <input id="q" placeholder="Search channel name" />
            <select id="state">
              <option value="all">All</option>
              <option value="enabled">Enabled</option>
              <option value="disabled">Disabled</option>
            </select>
          </div>
        </div>

        <div class="row" style="margin-top:10px">
          <button class="ghost" id="selAll" type="button"><i class="fa-regular fa-square-check"></i> Select All</button>
          <button class="ghost" id="selNone" type="button"><i class="fa-regular fa-square"></i> Select None</button>
          <button class="ghost" id="bulkEnable" type="button" disabled><i class="fa-solid fa-eye"></i> Enable Selected</button>
          <button class="ghost" id="bulkDisable" type="button" disabled><i class="fa-solid fa-eye-slash"></i> Disable Selected</button>
          <div class="pill" id="selCount" title="Click to clear selection (Esc)">0 selected</div>
          <div style="flex:1 1 auto"></div>
          <div class="pill" id="countPill">0 shown</div>
        </div>

        <div class="table" style="margin-top:10px">
		  <div class="tableWrap" id="tableWrap" tabindex="0" role="grid" aria-label="Channels table" aria-describedby="tableHelp">
			<div class="thead" style="grid-template-columns: 42px minmax(260px,1fr) minmax(160px,220px) minmax(140px,160px)">
			  <div>Select</div><div>Channel</div><div>Genre</div><div style="text-align:right">Status</div>
			</div>
			<div id="rows"></div>
		  </div>
		</div>
		<div class="small" id="tableHelp" style="margin-top:10px">Keyboard: Up/Down to move, Enter to open details, Space to toggle selection. Esc clears selection.</div>
      </div>
    </div>
  </div>

  <script>
    const $ = (id)=>document.getElementById(id);
    const toast = (title, msg, small='')=>{
      $('toastTitle').textContent=title;
      $('toastMsg').textContent=msg;
      $('toastSmall').textContent=small;
      $('toast').style.display='block';
      clearTimeout(window.__toastT);
      window.__toastT=setTimeout(()=>{try{$('toast').style.display='none'}catch(e){}}, 2400);
    };
    const postForm = async (url, obj)=>{
      const fd = new URLSearchParams();
      Object.keys(obj||{}).forEach(k=>fd.append(k, obj[k]));
      const res = await fetch(url, {method:'POST', headers:{'Content-Type':'application/x-www-form-urlencoded'}, body:fd});
      if(!res.ok){ throw new Error(await res.text()); }
      return res.json();
    };

	const showErr = (msg)=>{
		$('errMsg').textContent = msg || 'Unknown error';
		$('errBanner').style.display = 'block';
	};
	const clearErr = ()=>{
		$('errBanner').style.display = 'none';
		$('errMsg').textContent = '';
	};
	const safeJson = async (res)=>{
		const t = await res.text();
		try{ return JSON.parse(t); }catch(e){
			throw new Error(t || ('HTTP '+res.status));
		}
	};
	const __cache = new Map();
	const cacheGet = (k)=>{
		const v = __cache.get(k);
		if(!v) return null;
		if(Date.now() - v.t > 3500) return null;
		return v.val;
	};
	const cacheSet = (k, val)=>{ try{ __cache.set(k, {t: Date.now(), val}); }catch(e){} };
	const getJson = async (url)=>{
		clearErr();
		let res;
		try{
			res = await fetch(url, {cache:'no-store'});
		}catch(e){
			throw new Error('Network error. Check if stalkerhek is running.');
		}
		if(!res.ok){
			let body;
			try{ body = await safeJson(res); }catch(e){ body = {error: (e.message||'')}; }
			throw new Error(body && body.error ? body.error : ('Request failed (HTTP '+res.status+')'));
		}
		return safeJson(res);
	};
	const getJsonCached = async (url)=>{
		const hit = cacheGet(url);
		if(hit) return hit;
		const v = await getJson(url);
		cacheSet(url, v);
		return v;
	};

	const skeletonList = (n)=>{
		const arr=[];
		for(let i=0;i<n;i++){
			arr.push('<div class="item" aria-hidden="true"><div style="display:flex;gap:10px;align-items:center"><span class="ck" style="opacity:.2"><input type="checkbox" disabled></span><div style="flex:1 1 auto"><div class="name" style="height:14px;width:42%;background:rgba(31,46,35,.35);border-radius:8px"></div><div class="small" style="height:12px;width:62%;margin-top:8px;background:rgba(31,46,35,.22);border-radius:8px"></div></div></div><div class="pill" style="opacity:.25">…</div></div>');
		}
		return arr.join('');
	};
	const skeletonRows = (n)=>{
		const arr=[];
		for(let i=0;i<n;i++){
			arr.push('<div class="trow" aria-hidden="true" style="grid-template-columns:42px minmax(260px,1fr) minmax(160px,220px) minmax(140px,160px)"><div style="display:flex;justify-content:center"><span class="ck" style="opacity:.2"><input type="checkbox" disabled></span></div><div><div class="name" style="height:14px;width:46%;background:rgba(31,46,35,.35);border-radius:8px"></div><div class="small" style="height:12px;width:72%;margin-top:8px;background:rgba(31,46,35,.22);border-radius:8px"></div></div><div><div class="small" style="height:12px;width:40%;background:rgba(31,46,35,.22);border-radius:8px"></div><div class="small" style="height:12px;width:28%;margin-top:8px;background:rgba(31,46,35,.18);border-radius:8px"></div></div><div class="toggle"><div class="pill" style="opacity:.25">…</div></div></div>');
		}
		return arr.join('');
	};

	let __confirmFn = null;
	let __lastFocus = null;
	let __trapRoot = null;
	const focusables = (root)=>{
		try{
			if(!root) return [];
			return Array.from(root.querySelectorAll('a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])'))
				.filter(el=>!!(el && el.offsetParent!==null));
		}catch(e){ return []; }
	};
	const trapFocus = (root)=>{
		__trapRoot = root;
		try{
			const els = focusables(root);
			if(els.length>0) els[0].focus();
		}catch(e){}
	};
	const releaseFocus = ()=>{
		__trapRoot = null;
		try{ if(__lastFocus && typeof __lastFocus.focus==='function') __lastFocus.focus(); }catch(e){}
		__lastFocus = null;
	};
	const closeModal = ()=>{
		try{ $('modal').classList.remove('open'); $('modalBack').classList.remove('open'); }catch(e){}
		__confirmFn = null;
		releaseFocus();
	};
	const openModal = (title, body, confirmLabel, fn)=>{
		try{ __lastFocus = document.activeElement; }catch(e){}
		$('mTitle').textContent = title || 'Confirm';
		$('mBody').textContent = body || '';
		$('mConfirm').textContent = confirmLabel || 'Confirm';
		__confirmFn = fn || null;
		$('modal').classList.add('open');
		$('modalBack').classList.add('open');
		trapFocus($('modal'));
	};

	const hideTip = ()=>{ try{ $('tip').style.display='none'; }catch(e){} };
	const showTip = (title, lines, ev)=>{
		try{
			$('tipTitle').textContent = title||'';
			const box = $('tipLines');
			box.innerHTML='';
			(lines||[]).slice(0, 12).forEach(t=>{
				const d=document.createElement('div');
				d.textContent=String(t||'');
				box.appendChild(d);
			});
			$('tip').style.display='block';
			if(ev && typeof ev.clientX==='number'){
				const pad=14;
				const x = Math.min(window.innerWidth-40, ev.clientX+pad);
				const y = Math.min(window.innerHeight-40, ev.clientY+pad);
				$('tip').style.left = x+'px';
				$('tip').style.top = y+'px';
			}
		}catch(e){}
	};

	let __hoverTimer = null;
	let __hoverToken = 0;
	const hoverStart = (fn)=>{
		clearTimeout(__hoverTimer);
		__hoverToken++;
		const tok = __hoverToken;
		__hoverTimer = setTimeout(()=>{ try{ fn(tok); }catch(e){} }, 220);
		return tok;
	};
	const hoverStop = ()=>{
		clearTimeout(__hoverTimer);
		__hoverToken++;
		hideTip();
	};

	const catHoverCache = new Map();
	const genreHoverCache = new Map();
	const channelProbeCache = new Map();

    let state = {
		id: Number($('profileSel').value||0),
		stage: 'categories',
		category: '',
		genre_id: '',
		genre_name: '',
		q: '',
		view: 'all',
		selected: new Set(),
		catSelected: new Set(),
		genreSelected: new Set(),
		items: [],
		genres: [],
		cats: [],
	};
    let debTimer = null;

    const lsKey = ()=>('filters_view_'+String(state.id||0));
    const loadView = ()=>{
      try{
        const raw = localStorage.getItem(lsKey());
        if(!raw) return;
        const v = JSON.parse(raw);
        if(v && typeof v==='object'){
          if(typeof v.q==='string') $('q').value = v.q;
          if(typeof v.view==='string') $('state').value = v.view;
        }
      }catch(e){}
    };
    const saveView = ()=>{
      try{
        const v = {
          q: ($('q').value||'').trim(),
          view: ($('state').value||'all').trim()
        };
        localStorage.setItem(lsKey(), JSON.stringify(v));
      }catch(e){}
    };

    const setQueryFromUI = ()=>{
      state.id = Number($('profileSel').value||0);
      state.q = ($('q').value||'').trim();
      state.view = ($('state').value||'all').trim();
    };

    const renderChips = ()=>{
      setQueryFromUI();
      const chips = [];
      if(state.q) chips.push({k:'Search', v: state.q, clear: ()=>{ $('q').value=''; }});
      if(state.view && state.view !== 'all') chips.push({k:'State', v: state.view, clear: ()=>{ $('state').value='all'; }});
      if(state.category) chips.push({k:'Category', v: state.category, clear: ()=>{ state.category=''; }});
      if(state.genre_name) chips.push({k:'Genre', v: state.genre_name, clear: ()=>{ state.genre_id=''; state.genre_name=''; }});
      $('chips').innerHTML='';
      if(chips.length===0){
        $('chips').style.display='none';
        return;
      }
      $('chips').style.display='flex';
      chips.forEach(c=>{
        const el=document.createElement('div');
        el.className='chip';
        const t=document.createElement('div');
        t.textContent=c.k+': '+c.v;
        const b=document.createElement('button');
        b.className='ghost';
        b.type='button';
        b.innerHTML='<i class="fa-solid fa-xmark"></i>';
		b.onclick=()=>{ c.clear(); saveView(); syncStage(); };
        el.appendChild(t);
        el.appendChild(b);
        $('chips').appendChild(el);
      });
    };

    const loadCategories = async (tok)=>{
		$('cats').innerHTML = skeletonList(8);
		const arr = await getJsonCached('/api/filters/categories?id='+encodeURIComponent(state.id));
		if(isStale(tok)) return [];
		$('hint').textContent = 'Profile '+state.id+': filtering is live.';
		return Array.isArray(arr)?arr:[];
	};

    const loadGenres = async (tok)=>{
		$('genres').innerHTML = skeletonList(10);
		const u = new URLSearchParams({id: String(state.id)});
		if(state.category) u.set('category', state.category);
		const arr = await getJsonCached('/api/filters/genres?'+u.toString());
		if(isStale(tok)) return [];
		$('hint').textContent = 'Profile '+state.id+': filtering is live.';
		return Array.isArray(arr)?arr:[];
	};

    const groupKey = (name)=>{
      name = String(name||'').trim();
      if(!name) return 'Other';
      // Strong heuristic:
      // - If the portal uses a prefix delimiter (e.g. "MX| DAZN"), group by the left side.
      // - Otherwise group by the first meaningful token.
      // Normalize whitespace and delimiter spacing so "MX|DAZN" and "MX| DAZN" behave the same.
      const norm = name.replace(/\s+/g,' ').replace(/\s*\|\s*/g,'|').trim();
      if(norm.includes('|')){
        const left = norm.split('|')[0].trim();
        if(left) return left;
      }
      // Also handle common separators seen in IPTV portals.
      const norm2 = norm.replace(/[\/:>\-]+/g,' ').replace(/\s+/g,' ').trim();
      const parts = norm2.split(' ');
      const first = (parts[0]||'Other').trim();
      if(!first) return 'Other';
      if(first.length <= 3 && parts.length > 1) return (first + ' ' + (parts[1]||'')).trim();
      return first;
    };

    const renderCategories = (arr)=>{
      const q = ($('catFilter').value||'').toLowerCase().trim();
      $('cats').innerHTML = '';
      state.cats = arr||[];
      $('catSelCount').textContent = (state.catSelected?state.catSelected.size:0) + ' selected';
      $('catEnable').disabled = !state.catSelected || state.catSelected.size===0;
      $('catDisable').disabled = !state.catSelected || state.catSelected.size===0;
      $('catHint').textContent = 'Tip: click a category row to drill down. Use checkboxes for bulk actions.';
      (arr||[]).forEach(c=>{
        const name = (c.category||'Other');
        if(q && !String(name).toLowerCase().includes(q)) return;
        const row = document.createElement('div');
        row.className = 'item';
        row.style.cursor = 'pointer';
        row.onclick = ()=>{
          state.category = name;
          state.stage = 'genres';
          state.genre_id = '';
          state.genre_name = '';
          state.selected = new Set();
          state.genreSelected = new Set();
          syncStage();
        };
        row.onmouseenter = (ev)=>{
          hoverStart(async (tok)=>{
            const key = String(state.id)+'::'+String(name);
            let lines = catHoverCache.get(key);
            if(!lines){
              showTip('Category: '+name, ['Loading genres…'], ev);
              const u = new URLSearchParams({id: String(state.id), category: String(name)});
              const arr = await getJson('/api/filters/genres?'+u.toString());
              const gs = Array.isArray(arr)?arr:[];
              lines = gs.slice(0, 12).map(x=>{
                const n = (x && x.name) ? String(x.name) : 'Other';
                const en = (x && typeof x.enabled==='number') ? x.enabled : 0;
                const tot = (x && typeof x.total==='number') ? x.total : 0;
                return n+' — '+en+' / '+tot;
              });
              if(lines.length===0) lines = ['No genres'];
              catHoverCache.set(key, lines);
            }
            if(tok !== __hoverToken) return;
            showTip('Category: '+name, lines, ev);
          });
        };
        row.onmousemove = (ev)=>{ if($('tip').style.display==='block') showTip($('tipTitle').textContent, Array.from($('tipLines').children).map(n=>n.textContent||''), ev); };
        row.onmouseleave = ()=>hoverStop();
        const leftWrap = document.createElement('div');
        leftWrap.style.display='flex';
        leftWrap.style.gap='10px';
        leftWrap.style.alignItems='center';
        const ckWrap = document.createElement('span');
        ckWrap.className='ck';
        const chk = document.createElement('input');
        chk.type='checkbox';
        chk.checked = !!(state.catSelected && state.catSelected.has(name));
        chk.onclick = (ev)=>{ ev.stopPropagation(); };
        chk.onchange = ()=>{
          if(!state.catSelected) state.catSelected = new Set();
          if(chk.checked) state.catSelected.add(name); else state.catSelected.delete(name);
          $('catSelCount').textContent = state.catSelected.size + ' selected';
          $('catEnable').disabled = state.catSelected.size===0;
          $('catDisable').disabled = state.catSelected.size===0;
        };
        ckWrap.appendChild(chk);
        const left = document.createElement('div');
        left.innerHTML = '<div class="name">'+String(name).replace(/</g,'&lt;')+'</div><div class="small">'+(c.enabled||0)+' enabled / '+(c.total||0)+' total · '+(c.genres||0)+' genres</div>';
        leftWrap.appendChild(ckWrap);
        leftWrap.appendChild(left);
        const right = document.createElement('div');
        const pill = document.createElement('div');
        const dis = (c.disabled_genres||0);
        const totalG = (c.genres||0);
        const mixed = (dis > 0 && dis < totalG) || (dis === totalG && (c.enabled||0) > 0);
        pill.className = 'pill ' + (mixed ? 'mix' : (dis === totalG ? 'bad' : 'ok'));
        pill.textContent = mixed ? 'Mixed' : ((dis === totalG) ? 'Disabled' : 'Enabled');
        right.appendChild(pill);
        row.appendChild(leftWrap);
        row.appendChild(right);
        $('cats').appendChild(row);
      });
    };

    const renderGenres = (arr)=>{
      const q = ($('genreFilter').value||'').toLowerCase().trim();
      $('genres').innerHTML = '';
      state.genres = arr||[];

      $('genreSelCount').textContent = (state.genreSelected?state.genreSelected.size:0) + ' selected';
      $('genreEnable').disabled = !state.genreSelected || state.genreSelected.size===0;
      $('genreDisable').disabled = !state.genreSelected || state.genreSelected.size===0;
      $('genreHint').textContent = 'Tip: click a genre row to drill down. Use checkboxes for bulk actions.';

      (arr||[]).forEach(g=>{
        const name = (g.name||'Other');
        if(q && !String(name).toLowerCase().includes(q)) return;
        const gid = (g.genre_id||'');
        const row = document.createElement('div');
        row.className = 'item';
        row.style.cursor = 'pointer';
		row.onmouseenter = (ev)=>{
			hoverStart(async (tok)=>{
				const key = String(state.id)+'::'+String(gid);
				let lines = genreHoverCache.get(key);
				if(!lines){
					showTip('Genre: '+name, ['Loading channels…'], ev);
					const u = new URLSearchParams({
						id: String(state.id),
						genre_id: String(gid),
						query: '',
						state: 'all',
						offset: '0',
						limit: '12',
					});
					const j = await getJson('/api/filters/channels?'+u.toString());
					const items = (j && Array.isArray(j.items)) ? j.items : [];
					lines = items.slice(0, 12).map(x=>{
						const t = x && x.title ? String(x.title) : '';
						return (x && x.enabled) ? (t+' — Enabled') : (t+' — Disabled');
					});
					if(lines.length===0) lines = ['No channels'];
					genreHoverCache.set(key, lines);
				}
				if(tok !== __hoverToken) return;
				showTip('Genre: '+name, lines, ev);
			});
		};
		row.onmousemove = (ev)=>{ if($('tip').style.display==='block') showTip($('tipTitle').textContent, Array.from($('tipLines').children).map(n=>n.textContent||''), ev); };
		row.onmouseleave = ()=>hoverStop();
        row.onclick = ()=>{
          state.genre_id = gid;
          state.genre_name = name;
          state.stage = 'channels';
          state.selected = new Set();
          syncStage();
        };
        const leftWrap = document.createElement('div');
        leftWrap.style.display='flex';
        leftWrap.style.gap='10px';
        leftWrap.style.alignItems='center';
        const ckWrap = document.createElement('span');
        ckWrap.className='ck';
        const chk = document.createElement('input');
        chk.type='checkbox';
        chk.checked = !!(state.genreSelected && state.genreSelected.has(gid));
        chk.onclick = (ev)=>{ ev.stopPropagation(); };
        chk.onchange = ()=>{
          if(!state.genreSelected) state.genreSelected = new Set();
          if(chk.checked) state.genreSelected.add(gid); else state.genreSelected.delete(gid);
          $('genreSelCount').textContent = state.genreSelected.size + ' selected';
          $('genreEnable').disabled = state.genreSelected.size===0;
          $('genreDisable').disabled = state.genreSelected.size===0;
        };
        ckWrap.appendChild(chk);
        const left = document.createElement('div');
        left.innerHTML = '<div class="name">'+String(name).replace(/</g,'&lt;')+'</div><div class="small">'+(g.enabled||0)+' enabled / '+(g.total||0)+' total</div>';
        leftWrap.appendChild(ckWrap);
        leftWrap.appendChild(left);
        const right = document.createElement('div');
        const pill = document.createElement('div');
        const gmixed = !!g.disabled && (g.enabled||0) > 0;
        pill.className = 'pill ' + (gmixed ? 'mix' : (g.disabled ? 'bad':'ok'));
        pill.textContent = gmixed ? 'Mixed' : (g.disabled ? 'Disabled' : 'Enabled');
        right.appendChild(pill);
        row.appendChild(leftWrap);
        row.appendChild(right);
        $('genres').appendChild(row);
      });
      renderChips();
    };

    const loadChannels = async (tok)=>{
		setQueryFromUI();
		$('rows').innerHTML = skeletonRows(10);
		const u = new URLSearchParams({
			id: String(state.id),
			query: state.q||'',
			genre_id: state.genre_id||'',
			state: state.view||'all',
			offset: '0',
			limit: '0',
		});
		const j = await getJsonCached('/api/filters/channels?'+u.toString());
		if(isStale(tok)) return {total:0, items:[]};
		return {total: j.total||0, items: Array.isArray(j.items)?j.items:[]};
	};

    const closeDrawer = ()=>{
		releaseFocus();
		$('drawer').classList.remove('open');
		$('drawerBack').classList.remove('open');
	};
	const openDrawer = (it)=>{
		if(!it) return;
		try{ __lastFocus = document.activeElement; }catch(e){}
		$('dTitle').textContent = it.title||'';
		$('dSub').textContent = 'Changes apply immediately to playlist/streams/proxy.';
      $('dGenre').textContent = (it.genre||'Other') + ' ('+(it.genre_id||'')+')';
      $('dCmd').textContent = it.cmd||'';
      $('dState').textContent = it.enabled ? 'Enabled':'Disabled';
      $('dToggle').innerHTML = it.enabled ? '<i class="fa-solid fa-eye-slash"></i> Disable Channel' : '<i class="fa-solid fa-eye"></i> Enable Channel';
      $('dToggle').onclick = async ()=>{
        try{
          await postForm('/api/filters/toggle_channel', {id: String(state.id), cmd: it.cmd||'', disabled: it.enabled ? '1':'0'});
          toast('Saved', (it.enabled?'Disabled ':'Enabled ')+(it.title||''));
          closeDrawer();
          await reloadChannelsOnly();
          await reloadGenresOnly();
        }catch(e){ toast('Failed', 'Could not update channel', e.message||''); }
      };
      $('dCopy').onclick = async ()=>{
        try{ await navigator.clipboard.writeText(it.cmd||''); toast('Copied', 'CMD copied to clipboard'); }catch(e){ toast('Copy failed', 'Could not access clipboard'); }
      };
      		$('drawer').classList.add('open');
		$('drawerBack').classList.add('open');
		trapFocus($('drawer'));
	};

	let __stageToken = 0;
	const nextStageToken = ()=>{ __stageToken++; return __stageToken; };
	const isStale = (tok)=>tok !== __stageToken;
	const normalizeTok = (tok)=>{ return (typeof tok === 'number' && tok > 0) ? tok : nextStageToken(); };

	let __activeRow = 0;
	const setActiveRow = (idx, ensureVisible=false)=>{
		try{
			const items = state.items||[];
			if(items.length===0){ __activeRow = 0; return; }
			idx = Math.max(0, Math.min(items.length-1, Number(idx)||0));
			__activeRow = idx;
			const rows = Array.from(($('rows')||document.body).querySelectorAll('.trow[data-idx]'));
			rows.forEach(r=>{
				const rIdx = Number(r.getAttribute('data-idx')||'-1');
				const on = rIdx === __activeRow;
				r.classList.toggle('active', on);
				r.setAttribute('aria-selected', on ? 'true' : 'false');
			});
			if(ensureVisible){
				const el = $('rows').querySelector('.trow[data-idx="'+String(__activeRow)+'"]');
				if(el && el.scrollIntoView) el.scrollIntoView({block:'nearest'});
			}
		}catch(e){}
	};
	const shouldIgnoreKeys = (ev)=>{
		try{
			if(__trapRoot) return true;
			const t = ev && ev.target;
			if(!t) return false;
			const tag = (t.tagName||'').toLowerCase();
			if(tag==='input' || tag==='textarea' || tag==='select' || t.isContentEditable) return true;
		}catch(e){}
		return false;
	};
	document.addEventListener('keydown', (ev)=>{
		try{
			if(state.stage !== 'channels') return;
			if(shouldIgnoreKeys(ev)) return;
			if(ev.key === 'ArrowDown'){
				ev.preventDefault();
				setActiveRow(__activeRow+1, true);
				return;
			}
			if(ev.key === 'ArrowUp'){
				ev.preventDefault();
				setActiveRow(__activeRow-1, true);
				return;
			}
			if(ev.key === 'Enter'){
				const it = (state.items||[])[__activeRow];
				if(!it) return;
				ev.preventDefault();
				openDrawer(it);
				return;
			}
			if(ev.key === ' ') {
				const it = (state.items||[])[__activeRow];
				if(!it) return;
				ev.preventDefault();
				const cmd = it.cmd||'';
				if(!cmd) return;
				if(!state.selected) state.selected = new Set();
				if(state.selected.has(cmd)) state.selected.delete(cmd); else state.selected.add(cmd);
				refreshBulkBtns();
				renderChannels({items: state.items||[]});
				return;
			}
		}catch(e){}
	});
	document.addEventListener('keydown', (ev)=>{
		try{
			if(!__trapRoot) return;
			if(ev.key === 'Escape'){
				ev.preventDefault();
				if($('modal') && $('modal').classList.contains('open')) return closeModal();
				if($('drawer') && $('drawer').classList.contains('open')) return closeDrawer();
				return;
			}
			if(ev.key !== 'Tab') return;
			const els = focusables(__trapRoot);
			if(els.length===0) return;
			const first = els[0];
			const last = els[els.length-1];
			if(ev.shiftKey && document.activeElement === first){ ev.preventDefault(); last.focus(); return; }
			if(!ev.shiftKey && document.activeElement === last){ ev.preventDefault(); first.focus(); return; }
		}catch(e){}
	});

	window.addEventListener('error', (ev)=>{
		try{
			const msg = (ev && ev.message) ? String(ev.message) : 'Unexpected error';
			showErr(msg);
		}catch(e){}
	});
	window.addEventListener('unhandledrejection', (ev)=>{
		try{
			const r = ev && ev.reason;
			const msg = r && r.message ? String(r.message) : String(r||'Unhandled promise rejection');
			showErr(msg);
		}catch(e){}
	});

    const renderChannels = (resp)=>{
      const items = resp.items||[];
      state.items = items;
      $('rows').innerHTML = '';
      $('countPill').textContent = items.length+' shown';
      $('selCount').textContent = (state.selected ? state.selected.size : 0) + ' selected';
      $('bulkEnable').disabled = !state.selected || state.selected.size===0;
      $('bulkDisable').disabled = !state.selected || state.selected.size===0;

      renderChips();

      items.forEach((it, idx)=>{
        const tr = document.createElement('div');
        tr.className = 'trow';
        tr.setAttribute('data-idx', String(idx));
        tr.setAttribute('role', 'row');
        tr.setAttribute('tabindex', '-1');
        tr.setAttribute('aria-selected', 'false');
        tr.style.gridTemplateColumns = '42px minmax(260px,1fr) minmax(160px,220px) minmax(140px,160px)';
        const c1 = document.createElement('div');
        c1.setAttribute('role','gridcell');
        const ckWrap = document.createElement('span');
        ckWrap.className='ck';
        const chk = document.createElement('input');
        chk.type = 'checkbox';
        chk.checked = state.selected && state.selected.has(it.cmd||'');
        chk.onclick = (ev)=>{ ev.stopPropagation(); };
        chk.onchange = ()=>{
          const cmd = it.cmd||'';
          if(!cmd) return;
          if(!state.selected) state.selected = new Set();
          if(chk.checked) state.selected.add(cmd); else state.selected.delete(cmd);
          $('selCount').textContent = state.selected.size+' selected';
          $('bulkEnable').disabled = state.selected.size===0;
          $('bulkDisable').disabled = state.selected.size===0;
        };
        ckWrap.appendChild(chk);
        c1.appendChild(ckWrap);
        c1.style.display='flex';
        c1.style.justifyContent='center';
        const c2 = document.createElement('div');
        c2.setAttribute('role','gridcell');
        c2.innerHTML = '<div class="name">'+(it.title||'').replace(/</g,'&lt;')+'</div><div class="small mono">'+(it.cmd||'').replace(/</g,'&lt;')+'</div>';
        const cGenre = document.createElement('div');
        cGenre.setAttribute('role','gridcell');
        cGenre.innerHTML = '<div class="small">'+(it.genre||'Other').replace(/</g,'&lt;')+'</div><div class="small mono">'+(it.genre_id||'').replace(/</g,'&lt;')+'</div>';
        const cState = document.createElement('div');
        cState.className = 'toggle';
        cState.setAttribute('role','gridcell');
        const pill = document.createElement('div');
        pill.className = 'pill ' + (it.enabled ? 'ok':'bad');
        pill.textContent = it.enabled ? 'Enabled':'Disabled';
        cState.appendChild(pill);
        tr.appendChild(c1);
        tr.appendChild(c2);
		tr.appendChild(cGenre);
        tr.appendChild(cState);
		tr.onfocus = ()=>{ try{ setActiveRow(idx,false); }catch(e){} };
		tr.onmouseenter = (ev)=>{
			try{ setActiveRow(idx,false); }catch(e){}
			hoverStart(async (tok)=>{
				const title = it.title||'';
				const cmd = it.cmd||'';
				const baseLines = [
					(it.enabled ? 'Enabled: yes' : 'Enabled: no'),
					'Genre: '+(it.genre||'Other'),
					'CMD: '+cmd,
					'Probe: pending…'
				];
				showTip('Channel: '+title, baseLines, ev);
				const cacheKey = String(state.id)+'::'+String(cmd);
				const cached = channelProbeCache.get(cacheKey);
				const now = Date.now();
				if(cached && (now - cached.t) < 45_000){
					if(tok !== __hoverToken) return;
					const lines = baseLines.slice(0,3).concat(cached.lines||[]);
					showTip('Channel: '+title, lines, ev);
					return;
				}
				try{
					const u = new URLSearchParams({id: String(state.id), cmd: String(cmd)});
					const j = await getJson('/api/filters/probe_channel?'+u.toString());
					const lines = [];
					if(j && j.error){
						lines.push('Probe: failed');
						lines.push(String(j.error));
					}else{
						lines.push('Probe: '+(j && j.ok ? 'ok' : 'failed'));
						if(j && j.create_link_ms!=null) lines.push('create_link: '+j.create_link_ms+' ms');
						if(j && j.stream_status!=null) lines.push('stream: HTTP '+j.stream_status);
						if(j && j.content_type) lines.push('type: '+j.content_type);
					}
					channelProbeCache.set(cacheKey, {t: now, lines});
					if(tok !== __hoverToken) return;
					showTip('Channel: '+title, baseLines.slice(0,3).concat(lines), ev);
				}catch(e){
					const lines = ['Probe: failed', String(e.message||e)];
					channelProbeCache.set(cacheKey, {t: now, lines});
					if(tok !== __hoverToken) return;
					showTip('Channel: '+title, baseLines.slice(0,3).concat(lines), ev);
				}
			});
		};
		tr.onmousemove = (ev)=>{ if($('tip').style.display==='block') showTip($('tipTitle').textContent, Array.from($('tipLines').children).map(n=>n.textContent||''), ev); };
		tr.onmouseleave = ()=>hoverStop();
		tr.onclick = ()=>{ try{ setActiveRow(idx,false); }catch(e){}; openDrawer(it); };
        $('rows').appendChild(tr);
      });
		try{ setActiveRow(__activeRow,false); }catch(e){}
    };

    const reloadCategories = async (tok)=>{
		tok = normalizeTok(tok);
		const cs = await loadCategories(tok);
		if(isStale(tok)) return;
		renderCategories(cs);
	};

    const reloadGenresOnly = async (tok)=>{
		tok = normalizeTok(tok);
		const gs = await loadGenres(tok);
		if(isStale(tok)) return;
		renderGenres(gs);
	};

    const reloadChannelsOnly = async (tok)=>{
		tok = normalizeTok(tok);
		const resp = await loadChannels(tok);
		if(isStale(tok)) return;
		renderChannels(resp);
	};

    const syncStage = async ()=>{
		const tok = nextStageToken();
		renderChips();
		$('viewCategories').style.display = (state.stage==='categories') ? 'block' : 'none';
		$('viewGenres').style.display = (state.stage==='genres') ? 'block' : 'none';
		$('viewChannels').style.display = (state.stage==='channels') ? 'block' : 'none';
		$('backBtn').style.display = (state.stage==='categories') ? 'none' : 'inline-flex';
		if(state.stage==='categories'){
			$('crumb').textContent = 'Categories';
			await reloadCategories(tok);
			return;
		}
		if(state.stage==='genres'){
			$('crumb').textContent = 'Categories / '+state.category;
			await reloadGenresOnly(tok);
			return;
		}
		if(state.stage==='channels'){
			$('crumb').textContent = 'Categories / '+state.category+' / '+state.genre_name;
			await reloadChannelsOnly(tok);
			return;
		}
	};

    $('reloadBtn').onclick = ()=>syncStage();
    $('profileSel').onchange = ()=>{ state.id = Number($('profileSel').value||0); state.stage='categories'; state.category=''; state.genre_id=''; state.genre_name=''; state.selected=new Set(); loadView(); saveView(); syncStage(); };
    $('catFilter').oninput = ()=>renderCategories(state.cats||[]);
    $('genreFilter').oninput = ()=>renderGenres(state.genres||[]);
    $('state').onchange = ()=>{ saveView(); if(state.stage==='channels') reloadChannelsOnly(); renderChips(); };
    $('q').oninput = ()=>{
      clearTimeout(debTimer);
	  debTimer = setTimeout(()=>{ saveView(); if(state.stage==='channels') reloadChannelsOnly(); renderChips(); }, 250);
    };
	$('backBtn').onclick = ()=>{
		if(state.stage==='channels'){
			state.stage='genres';
			state.genre_id='';
			state.genre_name='';
			state.selected=new Set();
			state.genreSelected=new Set();
			return syncStage();
		}
		if(state.stage==='genres'){
			state.stage='categories';
			state.category='';
			state.selected=new Set();
			state.catSelected=new Set();
			return syncStage();
		}
	};

	$('modalBack').onclick = closeModal;
	$('mCancel').onclick = closeModal;
	$('mConfirm').onclick = async ()=>{
		const fn = __confirmFn;
		closeModal();
		if(fn) await fn();
	};

	$('catSelAll').onclick = ()=>{
		state.catSelected = new Set((state.cats||[]).map(x=>x.category||'').filter(Boolean));
		renderCategories(state.cats||[]);
	};
	$('catSelNone').onclick = ()=>{ state.catSelected = new Set(); renderCategories(state.cats||[]); };
	$('genreSelAll').onclick = ()=>{
		state.genreSelected = new Set((state.genres||[]).map(x=>x.genre_id||'').filter(Boolean));
		renderGenres(state.genres||[]);
	};
	$('genreSelNone').onclick = ()=>{ state.genreSelected = new Set(); renderGenres(state.genres||[]); };

	$('catEnable').onclick = async ()=>{
		try{
			if(!state.catSelected || state.catSelected.size===0) return;
			openModal('Enable categories?', 'This will enable all genres inside the selected categories. Selected: '+state.catSelected.size, 'Enable', async ()=>{
				const resp = await postForm('/api/filters/bulk_categories', {id:String(state.id), disabled:'0', categories: Array.from(state.catSelected).join('\n')});
				toast('Saved', 'Enabled categories', 'Updated '+(resp.genres||0)+' genre(s)');
				state.catSelected = new Set();
				await syncStage();
			});
		}catch(e){ showErr(e.message||'Could not update categories'); }
	};
	$('catDisable').onclick = async ()=>{
		try{
			if(!state.catSelected || state.catSelected.size===0) return;
			openModal('Disable categories?', 'This will disable all genres inside the selected categories. Selected: '+state.catSelected.size, 'Disable', async ()=>{
				const resp = await postForm('/api/filters/bulk_categories', {id:String(state.id), disabled:'1', categories: Array.from(state.catSelected).join('\n')});
				toast('Saved', 'Disabled categories', 'Updated '+(resp.genres||0)+' genre(s)');
				state.catSelected = new Set();
				await syncStage();
			});
		}catch(e){ showErr(e.message||'Could not update categories'); }
	};
	$('genreEnable').onclick = async ()=>{
		try{
			if(!state.genreSelected || state.genreSelected.size===0) return;
			openModal('Enable genres?', 'This will enable the selected genres. Selected: '+state.genreSelected.size, 'Enable', async ()=>{
				const resp = await postForm('/api/filters/bulk_genres', {id:String(state.id), disabled:'0', genre_ids: Array.from(state.genreSelected).join('\n')});
				toast('Saved', 'Enabled genres', 'Updated '+(resp.updated||0)+' genre(s)');
				state.genreSelected = new Set();
				await syncStage();
			});
		}catch(e){ showErr(e.message||'Could not update genres'); }
	};
	$('genreDisable').onclick = async ()=>{
		try{
			if(!state.genreSelected || state.genreSelected.size===0) return;
			openModal('Disable genres?', 'This will disable the selected genres. Selected: '+state.genreSelected.size, 'Disable', async ()=>{
				const resp = await postForm('/api/filters/bulk_genres', {id:String(state.id), disabled:'1', genre_ids: Array.from(state.genreSelected).join('\n')});
				toast('Saved', 'Disabled genres', 'Updated '+(resp.updated||0)+' genre(s)');
				state.genreSelected = new Set();
				await syncStage();
			});
		}catch(e){ showErr(e.message||'Could not update genres'); }
	};

	const refreshBulkBtns = ()=>{
		$('selCount').textContent = (state.selected?state.selected.size:0) + ' selected';
		$('bulkEnable').disabled = !state.selected || state.selected.size===0;
		$('bulkDisable').disabled = !state.selected || state.selected.size===0;
	};
	const clearChannelSelection = async (silent=false)=>{
		state.selected = new Set();
		if(!silent) toast('Selection cleared', 'No channels selected.');
		await reloadChannelsOnly();
	};
	$('selCount').onclick = async ()=>{
		if(state.stage !== 'channels') return;
		if(!state.selected || state.selected.size===0) return;
		await clearChannelSelection(true);
		toast('Selection cleared', 'No channels selected.');
	};
	document.addEventListener('keydown', async (ev)=>{
		try{
			if(ev.key !== 'Escape') return;
			if(state.stage !== 'channels') return;
			if(!state.selected || state.selected.size===0) return;
			ev.preventDefault();
			await clearChannelSelection(true);
			toast('Selection cleared', 'No channels selected.');
		}catch(e){}
	});
	$('selAll').onclick = ()=>{
		state.selected = new Set((state.items||[]).map(x=>x.cmd||'').filter(Boolean));
		reloadChannelsOnly();
	};
	$('selNone').onclick = ()=>{
		clearChannelSelection(true);
	};
	$('bulkEnable').onclick = async ()=>{
		try{
			if(!state.selected || state.selected.size===0) return;
			openModal('Enable channels?', 'This will enable '+state.selected.size+' selected channels.', 'Enable', async ()=>{
				const prev = new Map((state.items||[]).map(x=>[x.cmd||'', !!x.enabled]));
				try{
					(state.items||[]).forEach(x=>{ if(state.selected.has(x.cmd||'')) x.enabled = true; });
					reloadChannelsOnly();
					const resp = await postForm('/api/filters/bulk_channels', {id:String(state.id), disabled:'0', cmds: Array.from(state.selected).join('\n')});
					toast('Saved', 'Enabled channels', 'Updated '+(resp.updated||0)+' channel(s)');
				}catch(e){
					(state.items||[]).forEach(x=>{ const k=x.cmd||''; if(prev.has(k)) x.enabled = prev.get(k); });
					throw e;
				}
				state.selected = new Set();
				await reloadChannelsOnly();
				await reloadGenresOnly();
			});
		}catch(e){ showErr(e.message||'Could not update channels'); }
	};
	$('bulkDisable').onclick = async ()=>{
		try{
			if(!state.selected || state.selected.size===0) return;
			openModal('Disable channels?', 'This will disable '+state.selected.size+' selected channels.', 'Disable', async ()=>{
				const prev = new Map((state.items||[]).map(x=>[x.cmd||'', !!x.enabled]));
				try{
					(state.items||[]).forEach(x=>{ if(state.selected.has(x.cmd||'')) x.enabled = false; });
					reloadChannelsOnly();
					const resp = await postForm('/api/filters/bulk_channels', {id:String(state.id), disabled:'1', cmds: Array.from(state.selected).join('\n')});
					toast('Saved', 'Disabled channels', 'Updated '+(resp.updated||0)+' channel(s)');
				}catch(e){
					(state.items||[]).forEach(x=>{ const k=x.cmd||''; if(prev.has(k)) x.enabled = prev.get(k); });
					throw e;
				}
				state.selected = new Set();
				await reloadChannelsOnly();
				await reloadGenresOnly();
			});
		}catch(e){ showErr(e.message||'Could not update channels'); }
	};
    $('resetBtn').onclick = async ()=>{
      if(!confirm('Reset filters for this profile? This will re-enable all channels/genres.')) return;
      try{
        await postForm('/api/filters/reset', {id: String(state.id)});
        toast('Reset', 'All filters cleared for profile '+state.id);
		state.stage='categories'; state.category=''; state.genre_id=''; state.genre_name=''; state.selected=new Set();
		await syncStage();
      }catch(e){ toast('Failed', 'Could not reset filters', e.message||''); }
    };

    $('drawerBack').onclick = closeDrawer;
    $('drawerClose').onclick = closeDrawer;

    loadView();
    saveView();

	// Default entry point: categories.
	syncStage().catch(e=>{ showErr(e.message||'Failed to load filters'); });
  </script>
</body>
</html>`

		t := template.Must(template.New("filters").Parse(tpl))
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
