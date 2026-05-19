package webui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kidpoleon/stalkerhek/filterstore"
	"github.com/kidpoleon/stalkerhek/stalker"
)

// RegisterEPGVODHandlers mounts EPG and VOD endpoints on the WebUI mux.
func RegisterEPGVODHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/epg/", handleEPGRoute)
	mux.HandleFunc("/api/epg/", handleEPGAPI)
	mux.HandleFunc("/api/vod/", handleVODAPI)
	mux.HandleFunc("/vod/", handleVODRoute)
}

func handleEPGRoute(w http.ResponseWriter, r *http.Request) {
	// /epg/{id}/xmltv.xml
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "epg" {
		http.NotFound(w, r)
		return
	}
	pid := atoiSafe(parts[1])
	if parts[2] != "xmltv.xml" {
		http.NotFound(w, r)
		return
	}
	serveXMLTV(w, r, pid)
}

func handleEPGAPI(w http.ResponseWriter, r *http.Request) {
	// /api/epg/{id}/xmltv
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "epg" {
		http.NotFound(w, r)
		return
	}
	pid := atoiSafe(parts[2])
	if parts[3] == "xmltv" {
		serveXMLTV(w, r, pid)
		return
	}
	if parts[3] == "channel" && r.Method == http.MethodGet {
		title := strings.TrimSpace(r.URL.Query().Get("title"))
		if title == "" {
			http.Error(w, `{"error":"missing title"}`, http.StatusBadRequest)
			return
		}
		chs, _, ok := GetProfileChannels(pid)
		if !ok {
			http.Error(w, `{"error":"profile not running"}`, http.StatusNotFound)
			return
		}
		ch := chs[title]
		if ch == nil {
			http.Error(w, `{"error":"channel not found"}`, http.StatusNotFound)
			return
		}
		programs, err := ch.GetShortEPG(6)
		if err != nil || len(programs) == 0 {
			programs, err = ch.GetEPGInfo()
		}
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "programs": programs})
		return
	}
	http.NotFound(w, r)
}

func serveXMLTV(w http.ResponseWriter, r *http.Request, profileID int) {
	p, ok := GetProfile(profileID)
	if !ok {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}
	if u := strings.TrimSpace(p.EPGURL); u != "" {
		body, ct, err := FetchCustomEPG(profileID, u)
		if err != nil {
			LogWarn("EPG", "custom guide profile=%d err=%v", profileID, err)
			http.Error(w, "epg source unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(body)
		return
	}

	chs, _, ok := GetProfileChannels(profileID)
	if !ok {
		http.Error(w, "profile not running — start the profile to generate EPG from the portal", http.StatusServiceUnavailable)
		return
	}
	filtered := make(map[string]*stalker.Channel, len(chs))
	for title, ch := range chs {
		if ch != nil && filterstore.IsAllowed(profileID, ch) {
			filtered[title] = ch
		}
	}
	includePrograms := r.URL.Query().Get("programs") == "1"
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	data, err := stalker.BuildXMLTV(filtered, stalker.XMLTVOptions{
		IncludePrograms: includePrograms,
		ProgramLimit:    limit,
		EPGSize:         4,
	})
	if err != nil {
		LogWarn("EPG", "build xmltv profile=%d err=%v", profileID, err)
		http.Error(w, "failed to build epg", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", stalker.XMLTVContentType)
	w.Header().Set("Cache-Control", "public, max-age=120")
	_, _ = w.Write(data)
}

func handleVODAPI(w http.ResponseWriter, r *http.Request) {
	// /api/vod/{id}/categories | /list
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "vod" {
		http.NotFound(w, r)
		return
	}
	pid := atoiSafe(parts[2])
	portal, ok := GetProfilePortal(pid)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "profile not running"})
		return
	}
	switch parts[3] {
	case "categories":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		cats, err := portal.GetVODCategories()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		series, _ := portal.GetSeriesCategories()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "vod": cats, "series": series})
		return
	case "list":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		cat := strings.TrimSpace(r.URL.Query().Get("category"))
		if cat == "" {
			http.Error(w, `{"error":"missing category"}`, http.StatusBadRequest)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		items, total, err := portal.GetVODList(cat, page)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "total": total, "page": page, "items": items})
		return
	case "link":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		cmd := strings.TrimSpace(r.URL.Query().Get("cmd"))
		if cmd == "" {
			cmd = strings.TrimSpace(r.FormValue("cmd"))
		}
		if cmd == "" {
			http.Error(w, `{"error":"missing cmd"}`, http.StatusBadRequest)
			return
		}
		link, err := portal.NewVODLink(cmd)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": link})
		return
	}
	http.NotFound(w, r)
}

func handleVODRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "vod" {
		http.NotFound(w, r)
		return
	}
	pid := atoiSafe(parts[1])
	if parts[2] == "play" {
		cmd := strings.TrimSpace(r.URL.Query().Get("cmd"))
		if cmd == "" {
			http.Error(w, "missing cmd", http.StatusBadRequest)
			return
		}
		portal, ok := GetProfilePortal(pid)
		if !ok {
			http.Error(w, "profile not running", http.StatusServiceUnavailable)
			return
		}
		link, err := portal.NewVODLink(cmd)
		if err != nil {
			http.Error(w, "vod link failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, link, http.StatusFound)
		return
	}
	if parts[2] != "playlist.m3u" {
		http.NotFound(w, r)
		return
	}
	portal, ok := GetProfilePortal(pid)
	if !ok {
		http.Error(w, "profile not running", http.StatusServiceUnavailable)
		return
	}
	cats, err := portal.GetVODCategories()
	if err != nil {
		http.Error(w, "vod unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	scheme, host := requestSchemeHost(r)
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("#EXTM3U\n"))
	maxCats := 5
	if len(cats) < maxCats {
		maxCats = len(cats)
	}
	for i := 0; i < maxCats; i++ {
		items, _, err := portal.GetVODList(cats[i].ID, 0)
		if err != nil {
			continue
		}
		for _, it := range items {
			if it.CMD == "" {
				continue
			}
			name := stalker.CleanTitleForM3U8(it.Name)
			link := scheme + "://" + host + "/vod/" + strconv.Itoa(pid) + "/play?cmd=" + url.QueryEscape(it.CMD)
			_, _ = w.Write([]byte("#EXTINF:-1," + name + "\n" + link + "\n"))
		}
	}
}

func requestSchemeHost(r *http.Request) (string, string) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xf != "" {
		scheme = strings.TrimSpace(strings.Split(xf, ",")[0])
	}
	host := r.Host
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xf != "" {
		host = strings.TrimSpace(strings.Split(xf, ",")[0])
	}
	return scheme, host
}
