package webui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kidpoleon/stalkerhek/filterstore"
	"github.com/kidpoleon/stalkerhek/stalker"
)

var (
	portalXMLTVMu    sync.RWMutex
	portalXMLTVCache = map[int]portalXMLTVEntry{}
)

type portalXMLTVEntry struct {
	data    []byte
	updated time.Time
}

const portalXMLTVCacheTTL = 10 * time.Minute

// RegisterEPGVODHandlers mounts EPG and VOD endpoints on the WebUI mux.
func RegisterEPGVODHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/epg/", handleEPGRoute)
	mux.HandleFunc("/api/epg/", handleEPGAPI)
	mux.HandleFunc("/api/vod/", handleVODAPI)
	mux.HandleFunc("/vod/", handleVODRoute)
}

func handleEPGRoute(w http.ResponseWriter, r *http.Request) {
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

	epgURL := EffectiveEPGURL(p)
	if epgURL != "" {
		body, ct, err := FetchCustomEPG(profileID, epgURL)
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

	portal, portalOK := GetProfilePortal(profileID)
	chs, _, chOK := GetProfileChannels(profileID)
	if !chOK {
		http.Error(w, "profile not running — start the profile to generate EPG from the portal", http.StatusServiceUnavailable)
		return
	}

	includePrograms := true
	if r.URL.Query().Get("programs") == "0" {
		includePrograms = false
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}

	if includePrograms {
		if body, ok := cachedPortalXMLTV(profileID); ok {
			w.Header().Set("Content-Type", stalker.XMLTVContentType)
			w.Header().Set("Cache-Control", "public, max-age=120")
			_, _ = w.Write(body)
			return
		}
	}

	filtered := make(map[string]*stalker.Channel, len(chs))
	for title, ch := range chs {
		if ch != nil && filterstore.IsAllowed(profileID, ch) {
			filtered[title] = ch
		}
	}
	loc := time.UTC
	if portalOK && portal != nil {
		loc = portal.TimeLocation()
	}
	data, err := stalker.BuildXMLTV(filtered, stalker.XMLTVOptions{
		IncludePrograms: includePrograms,
		ProgramLimit:    limit,
		EPGSize:         8,
	}, loc)
	if err != nil {
		LogWarn("EPG", "build xmltv profile=%d err=%v", profileID, err)
		http.Error(w, "failed to build epg", http.StatusInternalServerError)
		return
	}
	if includePrograms {
		setPortalXMLTVCache(profileID, data)
	}
	w.Header().Set("Content-Type", stalker.XMLTVContentType)
	w.Header().Set("Cache-Control", "public, max-age=120")
	_, _ = w.Write(data)
}

func cachedPortalXMLTV(profileID int) ([]byte, bool) {
	portalXMLTVMu.RLock()
	e, ok := portalXMLTVCache[profileID]
	portalXMLTVMu.RUnlock()
	if !ok || time.Since(e.updated) > portalXMLTVCacheTTL {
		return nil, false
	}
	return e.data, true
}

func setPortalXMLTVCache(profileID int, data []byte) {
	portalXMLTVMu.Lock()
	portalXMLTVCache[profileID] = portalXMLTVEntry{data: data, updated: time.Now()}
	portalXMLTVMu.Unlock()
}

func InvalidatePortalXMLTVCache(profileID int) {
	portalXMLTVMu.Lock()
	delete(portalXMLTVCache, profileID)
	portalXMLTVMu.Unlock()
	InvalidateEPGCache(profileID)
}

func handleVODAPI(w http.ResponseWriter, r *http.Request) {
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
		if page < 0 {
			page = 0
		}
		kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
		var items []stalker.VODItem
		var total int
		var err error
		if kind == "series" {
			items, total, err = portal.GetSeriesList(cat, page)
		} else {
			items, total, err = portal.GetVODList(cat, page)
		}
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "total": total, "page": page, "items": items})
		return
	case "seasons":
		movieID := strings.TrimSpace(r.URL.Query().Get("movie_id"))
		if movieID == "" {
			http.Error(w, `{"error":"missing movie_id"}`, http.StatusBadRequest)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 0 {
			page = 0
		}
		items, total, err := portal.GetSeasons(movieID, page)
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "total": total, "items": items})
		return
	case "episodes":
		movieID := strings.TrimSpace(r.URL.Query().Get("movie_id"))
		seasonID := strings.TrimSpace(r.URL.Query().Get("season_id"))
		if movieID == "" || seasonID == "" {
			http.Error(w, `{"error":"missing movie_id or season_id"}`, http.StatusBadRequest)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 0 {
			page = 0
		}
		items, total, err := portal.GetEpisodes(movieID, seasonID, page)
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "total": total, "items": items})
		return
	case "link":
		cmd := strings.TrimSpace(r.URL.Query().Get("cmd"))
		seriesNum := strings.TrimSpace(r.URL.Query().Get("series"))
		linkType := strings.TrimSpace(r.URL.Query().Get("type"))
		if linkType == "" {
			if seriesNum != "" {
				linkType = "series"
			} else {
				linkType = "movie"
			}
		}
		pl, err := portal.CreateVODPlayLink(cmd, linkType, seriesNum)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": pl.URL, "play": pl})
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
		seriesNum := strings.TrimSpace(r.URL.Query().Get("series"))
		linkType := strings.TrimSpace(r.URL.Query().Get("type"))
		if linkType == "" {
			if seriesNum != "" {
				linkType = "series"
			} else {
				linkType = "movie"
			}
		}
		portal, ok := GetProfilePortal(pid)
		if !ok {
			http.Error(w, "profile not running", http.StatusServiceUnavailable)
			return
		}
		var pl *stalker.VODPlayLink
		var err error
		if cmd != "" {
			pl, err = portal.CreateVODPlayLink(cmd, linkType, seriesNum)
		} else {
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			pl, err = portal.CreateVODPlayLinkByID(id, linkType, seriesNum)
		}
		if err != nil {
			http.Error(w, "vod link failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, pl.URL, http.StatusFound)
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
	writeVODPlaylist(w, r, pid, portal)
}

func writeVODPlaylist(w http.ResponseWriter, r *http.Request, pid int, portal *stalker.Portal) {
	if portal == nil {
		http.Error(w, "portal unavailable", http.StatusServiceUnavailable)
		return
	}
	p, ok := GetProfile(pid)
	if !ok {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}
	user := strings.TrimSpace(p.MAC)
	if user == "" {
		http.Error(w, "profile MAC missing", http.StatusInternalServerError)
		return
	}
	scheme, host := requestSchemeHost(r)
	pass := xtreamPassword
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("#EXTM3U\n")); err != nil {
		LogWarn("VOD", "playlist write header profile=%d err=%v", pid, err)
		return
	}

	movieCats, err := portal.GetVODCategories()
	if err != nil {
		LogWarn("VOD", "movie categories profile=%d err=%v", pid, err)
	}
	for _, cat := range movieCats {
		items, err := portal.FetchAllVODMovies(cat.ID, maxVODPagesFetch)
		if err != nil {
			LogWarn("VOD", "movies cat=%s profile=%d err=%v", cat.ID, pid, err)
			continue
		}
		group := "Movies|" + stalker.CleanTitleForM3U8(cat.Title)
		for _, it := range items {
			ext := extOrDefault(it.ContainerExt, "mp4")
			link := scheme + "://" + host + "/movie/" + url.PathEscape(user) + "/" + pass + "/" + it.ID + "." + ext
			_, _ = w.Write([]byte("#EXTINF:-1 group-title=\"" + group + "\"," + stalker.CleanTitleForM3U8(it.Name) + "\n" + link + "\n"))
		}
	}

	seriesCats, err := portal.GetSeriesCategories()
	if err != nil {
		LogWarn("VOD", "series categories profile=%d err=%v", pid, err)
	}
	for _, cat := range seriesCats {
		shows, err := portal.FetchAllSeries(cat.ID, maxVODPagesFetch)
		if err != nil {
			LogWarn("VOD", "series cat=%s profile=%d err=%v", cat.ID, pid, err)
			continue
		}
		for _, show := range shows {
			seasons, _, _ := portal.GetSeasons(show.ID, 0)
			for _, sn := range seasons {
				sid := sn.SeasonID
				if sid == "" {
					sid = sn.ID
				}
				eps, err := portal.FetchAllEpisodes(show.ID, sid, maxVODPagesFetch)
				if err != nil {
					LogWarn("VOD", "episodes show=%s season=%s err=%v", show.ID, sid, err)
					continue
				}
				group := "Series|" + stalker.CleanTitleForM3U8(cat.Title) + "|" + stalker.CleanTitleForM3U8(show.Name)
				for _, ep := range eps {
					ext := extOrDefault(ep.ContainerExt, "mp4")
					epNum := ep.EpisodeNumber
					if epNum == "" {
						epNum = ep.ID
					}
					link := scheme + "://" + host + "/series/" + url.PathEscape(user) + "/" + pass + "/" + ep.ID + "." + ext + "?series=" + url.QueryEscape(epNum)
					_, _ = w.Write([]byte("#EXTINF:-1 group-title=\"" + group + "\"," + stalker.CleanTitleForM3U8(ep.Name) + "\n" + link + "\n"))
				}
			}
		}
	}
}
