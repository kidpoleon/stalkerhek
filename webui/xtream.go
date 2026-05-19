package webui

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/kidpoleon/stalkerhek/filterstore"
	"github.com/kidpoleon/stalkerhek/stalker"
)

const (
	xtreamPassword   = "stalkerhek"
	maxVODPagesFetch = 50
)

// RegisterXtreamHandlers exposes Xtream Codes API and playback paths for IPTV apps.
func RegisterXtreamHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/player_api.php", handleXtreamPlayerAPI)
	mux.HandleFunc("/panel_api.php", handleXtreamPlayerAPI)
	mux.HandleFunc("/xtream/", handleXtreamRoute)
	mux.HandleFunc("/movie/", handleXtreamMovie)
	mux.HandleFunc("/series/", handleXtreamSeries)
	mux.HandleFunc("/live/", handleXtreamLive)
}

func handleXtreamPlayerAPI(w http.ResponseWriter, r *http.Request) {
	pid, ok := profileIDFromXtreamUser(r.URL.Query().Get("username"))
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"user_info": map[string]any{"auth": 0}})
		return
	}
	if !validateXtreamPassword(r.URL.Query().Get("password")) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"user_info": map[string]any{"auth": 0}})
		return
	}
	dispatchXtreamAction(w, r, pid)
}

func handleXtreamRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "xtream" {
		http.NotFound(w, r)
		return
	}
	pid := atoiSafe(parts[1])
	if parts[2] != "player_api.php" {
		http.NotFound(w, r)
		return
	}
	if !validateXtreamAuth(pid, r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"user_info": map[string]any{"auth": 0}})
		return
	}
	dispatchXtreamAction(w, r, pid)
}

func dispatchXtreamAction(w http.ResponseWriter, r *http.Request, profileID int) {
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	if action == "" {
		serveXtreamUserInfo(w, r, profileID)
		return
	}
	switch action {
	case "get_live_categories":
		serveXtreamLiveCategories(w, profileID)
	case "get_live_streams":
		serveXtreamLiveStreams(w, r, profileID)
	case "get_vod_categories":
		serveXtreamVODCategories(w, profileID, false)
	case "get_vod_streams":
		serveXtreamVODStreams(w, r, profileID)
	case "get_series_categories":
		serveXtreamVODCategories(w, profileID, true)
	case "get_series":
		serveXtreamSeries(w, r, profileID)
	case "get_series_info":
		serveXtreamSeriesInfo(w, r, profileID)
	case "get_short_epg", "get_simple_data_table":
		serveXtreamShortEPG(w, r, profileID)
	default:
		writeJSON(w, []any{})
	}
}

func profileIDFromXtreamUser(username string) (int, bool) {
	mac := normalizeMAC(username)
	if mac == "" {
		return 0, false
	}
	for _, p := range ListProfiles() {
		if normalizeMAC(p.MAC) == mac {
			return p.ID, true
		}
	}
	return 0, false
}

func validateXtreamAuth(profileID int, r *http.Request) bool {
	p, ok := GetProfile(profileID)
	if !ok {
		return false
	}
	user := strings.TrimSpace(r.URL.Query().Get("username"))
	if user == "" {
		return true
	}
	if normalizeMAC(user) != normalizeMAC(p.MAC) {
		return false
	}
	return validateXtreamPassword(r.URL.Query().Get("password"))
}

func validateXtreamPassword(pass string) bool {
	pass = strings.TrimSpace(pass)
	return pass == "" || pass == xtreamPassword
}

func normalizeMAC(mac string) string {
	mac = strings.ToUpper(strings.TrimSpace(mac))
	mac = strings.ReplaceAll(mac, "-", ":")
	return mac
}

func serveXtreamUserInfo(w http.ResponseWriter, r *http.Request, profileID int) {
	p, ok := GetProfile(profileID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	scheme, host := requestSchemeHost(r)
	epgURL := scheme + "://" + host + "/epg/" + strconv.Itoa(profileID) + "/xmltv.xml?programs=1&limit=500"
	if u := EffectiveEPGURL(p); u != "" {
		epgURL = u
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"user_info": map[string]any{
			"username":               p.MAC,
			"password":               xtreamPassword,
			"message":                "stalkerhek",
			"auth":                   1,
			"status":                 "Active",
			"exp_date":               "1999999999",
			"is_trial":               "0",
			"active_cons":            "0",
			"created_at":             "1600000000",
			"max_connections":        "1",
			"allowed_output_formats": []string{"m3u8", "ts", "mp4"},
		},
		"server_info": map[string]any{
			"url":             scheme + "://" + host,
			"port":            xtreamPort(host),
			"https_port":      "443",
			"server_protocol": scheme,
			"rtmp_port":       "",
			"timezone":        p.TimeZone,
			"timestamp_now":   true,
			"time_now":        "",
			"epg_url":         epgURL,
		},
	})
}

func xtreamPort(host string) string {
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[i+1:]
	}
	return "4400"
}

func serveXtreamLiveCategories(w http.ResponseWriter, profileID int) {
	chs, _, ok := GetProfileChannels(profileID)
	if !ok {
		writeJSON(w, []any{})
		return
	}
	seen := map[string]struct{}{}
	var cats []map[string]any
	idx := 1
	for _, ch := range chs {
		if ch == nil || !filterstore.IsAllowed(profileID, ch) {
			continue
		}
		g := stalker.CleanGenreForM3U8(ch.RawGenre())
		if g == "" {
			g = "Other"
		}
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		cats = append(cats, map[string]any{
			"category_id":   strconv.Itoa(idx),
			"category_name": g,
			"parent_id":     0,
		})
		idx++
	}
	writeJSON(w, cats)
}

func serveXtreamLiveStreams(w http.ResponseWriter, r *http.Request, profileID int) {
	_, ok := GetProfile(profileID)
	if !ok {
		writeJSON(w, []any{})
		return
	}
	chs, keys, ok := GetProfileChannels(profileID)
	if !ok {
		writeJSON(w, []any{})
		return
	}
	catFilter := strings.TrimSpace(r.URL.Query().Get("category_id"))
	catMap := buildCategoryIDMap(profileID, chs)
	var out []map[string]any
	num := 1
	for _, title := range keys {
		ch := chs[title]
		if ch == nil || !filterstore.IsAllowed(profileID, ch) {
			continue
		}
		g := stalker.CleanGenreForM3U8(ch.RawGenre())
		if g == "" {
			g = "Other"
		}
		cid := catMap[g]
		if catFilter != "" && catFilter != cid {
			continue
		}
		out = append(out, map[string]any{
			"num":                 num,
			"name":                stalker.CleanTitleForM3U8(title),
			"stream_type":         "live",
			"stream_id":           num,
			"stream_icon":         ch.Logo(),
			"epg_channel_id":      ch.EPGChannelID(),
			"added":               "0",
			"category_id":         cid,
			"custom_sid":          "",
			"tv_archive":          0,
			"direct_source":       "",
			"tv_archive_duration": 0,
		})
		num++
	}
	writeJSON(w, out)
}

func buildCategoryIDMap(profileID int, chs map[string]*stalker.Channel) map[string]string {
	seen := map[string]struct{}{}
	out := map[string]string{}
	idx := 1
	for _, ch := range chs {
		if ch == nil || !filterstore.IsAllowed(profileID, ch) {
			continue
		}
		g := stalker.CleanGenreForM3U8(ch.RawGenre())
		if g == "" {
			g = "Other"
		}
		if _, ok := seen[g]; ok {
			continue
		}
		seen[g] = struct{}{}
		out[g] = strconv.Itoa(idx)
		idx++
	}
	return out
}

func serveXtreamVODCategories(w http.ResponseWriter, profileID int, series bool) {
	portal, ok := GetProfilePortal(profileID)
	if !ok {
		writeJSON(w, []any{})
		return
	}
	var cats []stalker.VODCategory
	var err error
	if series {
		cats, err = portal.GetSeriesCategories()
	} else {
		cats, err = portal.GetVODCategories()
	}
	if err != nil {
		log.Printf("[XTREAM] profile=%d vod categories err=%v", profileID, err)
		writeJSON(w, []any{})
		return
	}
	var out []map[string]any
	for _, c := range cats {
		out = append(out, map[string]any{
			"category_id":   c.ID,
			"category_name": c.Title,
			"parent_id":     0,
		})
	}
	writeJSON(w, out)
}

func serveXtreamVODStreams(w http.ResponseWriter, r *http.Request, profileID int) {
	portal, ok := GetProfilePortal(profileID)
	if !ok {
		writeJSON(w, []any{})
		return
	}
	cat := strings.TrimSpace(r.URL.Query().Get("category_id"))
	if cat == "" {
		writeJSON(w, []any{})
		return
	}
	items, err := portal.FetchAllVODMovies(cat, maxVODPagesFetch)
	if err != nil {
		log.Printf("[XTREAM] profile=%d vod streams cat=%s err=%v", profileID, cat, err)
		writeJSON(w, []any{})
		return
	}
	p, _ := GetProfile(profileID)
	user := ""
	if p.MAC != "" {
		user = p.MAC
	}
	writeJSON(w, xtreamMovieItems(user, items, cat))
}

func serveXtreamSeries(w http.ResponseWriter, r *http.Request, profileID int) {
	portal, ok := GetProfilePortal(profileID)
	if !ok {
		writeJSON(w, []any{})
		return
	}
	cat := strings.TrimSpace(r.URL.Query().Get("category_id"))
	if cat == "" {
		writeJSON(w, []any{})
		return
	}
	items, err := portal.FetchAllSeries(cat, maxVODPagesFetch)
	if err != nil {
		log.Printf("[XTREAM] profile=%d series cat=%s err=%v", profileID, cat, err)
		writeJSON(w, []any{})
		return
	}
	var out []map[string]any
	for i, it := range items {
		out = append(out, map[string]any{
			"num":              i + 1,
			"name":             it.Name,
			"series_id":        it.ID,
			"cover":            it.Logo,
			"plot":             "",
			"cast":             "",
			"director":         "",
			"genre":            "",
			"releaseDate":      "",
			"last_modified":    "",
			"rating":           "",
			"rating_5based":    0,
			"backdrop_path":    []string{},
			"youtube_trailer":  "",
			"episode_run_time": "",
			"category_id":      cat,
			"stream_type":      "series",
		})
	}
	writeJSON(w, out)
}

func serveXtreamSeriesInfo(w http.ResponseWriter, r *http.Request, profileID int) {
	portal, ok := GetProfilePortal(profileID)
	if !ok {
		writeJSON(w, map[string]any{})
		return
	}
	seriesID := strings.TrimSpace(r.URL.Query().Get("series_id"))
	if seriesID == "" {
		seriesID = strings.TrimSpace(r.URL.Query().Get("series"))
	}
	if seriesID == "" {
		writeJSON(w, map[string]any{})
		return
	}
	seasons, _, err := portal.GetSeasons(seriesID, 0)
	if err != nil || len(seasons) == 0 {
		log.Printf("[XTREAM] profile=%d series_info id=%s seasons err=%v", profileID, seriesID, err)
		writeJSON(w, map[string]any{"seasons": []any{}, "info": map[string]any{}, "episodes": map[string]any{}})
		return
	}
	p, _ := GetProfile(profileID)
	user := p.MAC
	seasonList := make([]map[string]any, 0, len(seasons))
	episodes := map[string][]map[string]any{}
	for _, sn := range seasons {
		sid := sn.SeasonID
		if sid == "" {
			sid = sn.ID
		}
		seasonList = append(seasonList, map[string]any{
			"air_date":      "",
			"episode_count": 0,
			"id":            sid,
			"name":          sn.Name,
			"overview":      "",
			"season_number": sid,
			"cover":         sn.Logo,
			"cover_big":     sn.Logo,
		})
		eps, err := portal.FetchAllEpisodes(seriesID, sid, maxVODPagesFetch)
		if err != nil {
			continue
		}
		var epOut []map[string]any
		for _, ep := range eps {
			epNum := ep.EpisodeNumber
			if epNum == "" {
				epNum = ep.ID
			}
			ext := extOrDefault(ep.ContainerExt, "mp4")
			epOut = append(epOut, map[string]any{
				"id":                  ep.ID,
				"episode_num":         epNum,
				"title":               ep.Name,
				"container_extension": ext,
				"info":                map[string]any{},
				"custom_sid":          "",
				"added":               "0",
				"season":              sid,
				"direct_source":       "",
			})
			_ = user
		}
		episodes[sid] = epOut
	}
	writeJSON(w, map[string]any{
		"seasons":  seasonList,
		"info":     map[string]any{"name": "", "cover": "", "plot": ""},
		"episodes": episodes,
	})
}

func serveXtreamShortEPG(w http.ResponseWriter, r *http.Request, profileID int) {
	streamID := strings.TrimSpace(r.URL.Query().Get("stream_id"))
	chs, keys, ok := GetProfileChannels(profileID)
	if !ok {
		writeJSON(w, map[string]any{})
		return
	}
	idx, _ := strconv.Atoi(streamID)
	if idx < 1 || idx > len(keys) {
		writeJSON(w, map[string]any{})
		return
	}
	ch := chs[keys[idx-1]]
	if ch == nil {
		writeJSON(w, map[string]any{})
		return
	}
	programs, err := ch.GetShortEPG(8)
	if err != nil || len(programs) == 0 {
		programs, _ = ch.GetEPGInfo()
	}
	loc := ch.Portal.TimeLocation()
	var listings []map[string]any
	for _, pr := range programs {
		listings = append(listings, map[string]any{
			"id":              pr.Title,
			"epg_id":          ch.EPGChannelID(),
			"title":           pr.Title,
			"lang":            "en",
			"start":           pr.Start.In(loc).Format("2006-01-02 15:04:05"),
			"end":             pr.Stop.In(loc).Format("2006-01-02 15:04:05"),
			"description":     pr.Description,
			"channel_id":      ch.EPGChannelID(),
			"start_timestamp": pr.Start.Unix(),
			"stop_timestamp":  pr.Stop.Unix(),
		})
	}
	writeJSON(w, map[string]any{"epg_listings": listings})
}

func xtreamMovieItems(user string, items []stalker.VODItem, cat string) []map[string]any {
	var out []map[string]any
	for i, it := range items {
		ext := extOrDefault(it.ContainerExt, "mp4")
		out = append(out, map[string]any{
			"num":                 i + 1,
			"name":                it.Name,
			"stream_type":         "movie",
			"stream_id":           it.ID,
			"stream_icon":         it.Logo,
			"rating":              "",
			"rating_5based":       0,
			"added":               "0",
			"category_id":         cat,
			"container_extension": ext,
			"custom_sid":          "",
			"direct_source":       "",
		})
		_ = user
	}
	return out
}

// --- Xtream playback URLs: /movie/{user}/{pass}/{id}.ext ---

func handleXtreamMovie(w http.ResponseWriter, r *http.Request) {
	playXtreamVOD(w, r, "movie")
}

func handleXtreamSeries(w http.ResponseWriter, r *http.Request) {
	playXtreamVOD(w, r, "series")
}

func playXtreamVOD(w http.ResponseWriter, r *http.Request, linkType string) {
	if w == nil || r == nil {
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		http.NotFound(w, r)
		return
	}
	pid, ok := profileIDFromXtreamUser(parts[1])
	if !ok || !validateXtreamPassword(parts[2]) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	streamID := strings.TrimSpace(strings.TrimSuffix(parts[3], path.Ext(parts[3])))
	if streamID == "" {
		http.Error(w, "missing stream id", http.StatusBadRequest)
		return
	}
	seriesNum := strings.TrimSpace(r.URL.Query().Get("series"))
	portal, ok := GetProfilePortal(pid)
	if !ok || portal == nil {
		http.Error(w, "profile not running", http.StatusServiceUnavailable)
		return
	}
	var pl *stalker.VODPlayLink
	var err error
	if cmd := strings.TrimSpace(r.URL.Query().Get("cmd")); cmd != "" {
		pl, err = portal.CreateVODPlayLink(cmd, linkType, seriesNum)
	} else {
		pl, err = portal.CreateVODPlayLinkByID(streamID, linkType, seriesNum)
	}
	if err != nil {
		log.Printf("[XTREAM] play profile=%d type=%s id=%s err=%v", pid, linkType, streamID, err)
		http.Error(w, "playback failed", http.StatusBadGateway)
		return
	}
	if pl == nil || strings.TrimSpace(pl.URL) == "" {
		log.Printf("[XTREAM] play profile=%d type=%s id=%s empty url", pid, linkType, streamID)
		http.Error(w, "empty playback url", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, pl.URL, http.StatusFound)
}

func handleXtreamLive(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		http.NotFound(w, r)
		return
	}
	pid, ok := profileIDFromXtreamUser(parts[1])
	if !ok || !validateXtreamPassword(parts[2]) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	streamID, _ := strconv.Atoi(strings.TrimSuffix(parts[3], path.Ext(parts[3])))
	p, ok := GetProfile(pid)
	if !ok {
		http.NotFound(w, r)
		return
	}
	chs, keys, ok := GetProfileChannels(pid)
	if !ok || streamID < 1 || streamID > len(keys) {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	title := keys[streamID-1]
	ch := chs[title]
	if ch == nil || !filterstore.IsAllowed(pid, ch) {
		http.Error(w, "channel blocked", http.StatusForbidden)
		return
	}
	scheme, host := requestSchemeHost(r)
	redirect := scheme + "://" + host + ":" + strconv.Itoa(p.HlsPort) + "/iptv/" + url.PathEscape(title)
	http.Redirect(w, r, redirect, http.StatusFound)
}

