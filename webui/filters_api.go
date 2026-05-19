package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kidpoleon/stalkerhek/filterstore"
	"github.com/kidpoleon/stalkerhek/stalker"
)

type genreInfo struct {
	GenreID   string `json:"genre_id"`
	Category  string `json:"category"`
	Name      string `json:"name"`
	Total     int    `json:"total"`
	Disabled  bool   `json:"disabled"`
	Enabled   int    `json:"enabled"`
	Blocked   int    `json:"blocked"`
}

type categoryInfo struct {
	Category string `json:"category"`
	Total    int    `json:"total"`
	Enabled  int    `json:"enabled"`
	Blocked  int    `json:"blocked"`
	Genres   int    `json:"genres"`
	Disabled int    `json:"disabled_genres"`
}

type channelInfo struct {
	Title         string `json:"title"`
	CMD           string `json:"cmd"`
	GenreID       string `json:"genre_id"`
	Genre         string `json:"genre"`
	Enabled       bool   `json:"enabled"`
}

type channelsResp struct {
	Total int           `json:"total"`
	Items []channelInfo `json:"items"`
}

func RegisterFilterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/api/filters/categories", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		pid := atoiSafe(strings.TrimSpace(r.URL.Query().Get("id")))
		chs, _, ok := GetProfileChannels(pid)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "profile not running or channels not loaded"})
			return
		}
		m := map[string]*categoryInfo{}
		// Also track distinct genre IDs per category.
		genreSeen := map[string]map[string]struct{}{}
		for _, ch := range chs {
			if ch == nil {
				continue
			}
			cat := stalker.DeriveCategory(strings.TrimSpace(ch.Genre()))
			ci := m[cat]
			if ci == nil {
				ci = &categoryInfo{Category: cat}
				m[cat] = ci
			}
			ci.Total++
			if filterstore.IsAllowed(pid, ch) {
				ci.Enabled++
			} else {
				ci.Blocked++
			}
			gid := strings.TrimSpace(ch.GenreID)
			if gid == "" {
				gid = "Other"
			}
			s := genreSeen[cat]
			if s == nil {
				s = map[string]struct{}{}
				genreSeen[cat] = s
			}
			s[gid] = struct{}{}
		}
		for cat, set := range genreSeen {
			ci := m[cat]
			if ci == nil {
				continue
			}
			ci.Genres = len(set)
			dis := 0
			for gid := range set {
				if filterstore.IsGenreDisabled(pid, gid) {
					dis++
				}
			}
			ci.Disabled = dis
		}
		arr := make([]categoryInfo, 0, len(m))
		for _, v := range m {
			arr = append(arr, *v)
		}
		sort.Slice(arr, func(i, j int) bool {
			ki, kj := stalker.CategorySortKey(arr[i].Category), stalker.CategorySortKey(arr[j].Category)
			if ki != kj {
				return ki < kj
			}
			return stalker.CompareNatural(arr[i].Category, arr[j].Category) < 0
		})
		_ = json.NewEncoder(w).Encode(arr)
	})

	mux.HandleFunc("/api/filters/probe_channel", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		pid := atoiSafe(strings.TrimSpace(r.URL.Query().Get("id")))
		cmd := strings.TrimSpace(r.URL.Query().Get("cmd"))
		if cmd == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "missing cmd"})
			return
		}
		chs, _, ok := GetProfileChannels(pid)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "profile not running or channels not loaded"})
			return
		}
		var ch *stalker.Channel
		for _, c := range chs {
			if c == nil {
				continue
			}
			if strings.TrimSpace(c.CMD) == cmd {
				ch = c
				break
			}
		}
		if ch == nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "channel not found"})
			return
		}

		// Probe uses the portal's create_link to verify authentication and returns a playable URL,
		// then performs a quick HEAD/GET to check if the stream responds.
		start := time.Now()
		link, err := ch.NewLink(false)
		ms := time.Since(start).Milliseconds()
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "create_link_ms": ms, "error": err.Error()})
			return
		}

		client := &http.Client{Timeout: 4 * time.Second}
		req, err := http.NewRequest("HEAD", link, nil)
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "create_link_ms": ms, "error": err.Error()})
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			// Some IPTV origins don't support HEAD; try GET with a tiny read.
			req2, err2 := http.NewRequest("GET", link, nil)
			if err2 != nil {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "create_link_ms": ms, "error": err.Error()})
				return
			}
			resp2, err2 := client.Do(req2)
			if err2 != nil {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "create_link_ms": ms, "error": err2.Error()})
				return
			}
			defer resp2.Body.Close()
			_, _ = io.CopyN(io.Discard, resp2.Body, 256)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": resp2.StatusCode >= 200 && resp2.StatusCode < 400, "create_link_ms": ms, "stream_status": resp2.StatusCode, "content_type": resp2.Header.Get("Content-Type")})
			return
		}
		defer resp.Body.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": resp.StatusCode >= 200 && resp.StatusCode < 400, "create_link_ms": ms, "stream_status": resp.StatusCode, "content_type": resp.Header.Get("Content-Type")})
	})

	mux.HandleFunc("/api/filters/genres", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		pid := atoiSafe(strings.TrimSpace(r.URL.Query().Get("id")))
		catFilter := strings.TrimSpace(r.URL.Query().Get("category"))
		chs, _, ok := GetProfileChannels(pid)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "profile not running or channels not loaded"})
			return
		}
		// Count channels per genre.
		m := map[string]*genreInfo{}
		for _, ch := range chs {
			if ch == nil {
				continue
			}
			gid := strings.TrimSpace(ch.GenreID)
			if gid == "" {
				gid = "Other"
			}
			gname := strings.TrimSpace(ch.Genre())
			cat := stalker.DeriveCategory(gname)
			if catFilter != "" && cat != catFilter {
				continue
			}
			gi := m[gid]
			if gi == nil {
				gi = &genreInfo{GenreID: gid, Name: gname, Category: cat}
				if gi.Name == "" {
					gi.Name = "Other"
					gi.Category = "Other"
				}
				m[gid] = gi
			}
			gi.Total++
			if filterstore.IsAllowed(pid, ch) {
				gi.Enabled++
			} else {
				gi.Blocked++
			}
		}
		arr := make([]genreInfo, 0, len(m))
		for _, gi := range m {
			gi.Disabled = filterstore.IsGenreDisabled(pid, gi.GenreID)
			arr = append(arr, *gi)
		}
		sort.Slice(arr, func(i, j int) bool {
			ci, cj := stalker.CategorySortKey(arr[i].Category), stalker.CategorySortKey(arr[j].Category)
			if ci != cj {
				return ci < cj
			}
			if arr[i].Category != arr[j].Category {
				return stalker.CompareNatural(arr[i].Category, arr[j].Category) < 0
			}
			return stalker.CompareNatural(arr[i].Name, arr[j].Name) < 0
		})
		_ = json.NewEncoder(w).Encode(arr)
	})

	mux.HandleFunc("/api/filters/channels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		pid := atoiSafe(strings.TrimSpace(r.URL.Query().Get("id")))
		query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query")))
		genreID := strings.TrimSpace(r.URL.Query().Get("genre_id"))
		state := strings.TrimSpace(r.URL.Query().Get("state")) // all|enabled|disabled
		if state == "" {
			state = "all"
		}
		off, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("offset")))
		lim, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
		// Server-side protection for very large profiles (30k+ channels).
		// Allow large pages when requested, but never allow unbounded fetches
		// unless the request is scoped (genre or query) and still capped.
		const (
			defaultLimit   = 200
			maxPageLimit   = 5000
			maxAllInScope  = 50000
			maxOffsetLimit = 1000000
		)
		if off < 0 {
			off = 0
		}
		if off > maxOffsetLimit {
			off = maxOffsetLimit
		}
		scoped := strings.TrimSpace(genreID) != "" || query != ""
		if lim == 0 {
			if scoped {
				lim = maxAllInScope
			} else {
				lim = defaultLimit
			}
		}
		if lim < 0 {
			lim = defaultLimit
		}
		if lim > maxPageLimit {
			if scoped {
				lim = maxAllInScope
			} else {
				lim = maxPageLimit
			}
		}
		if lim <= 0 {
			lim = defaultLimit
		}

		chs, keys, ok := GetProfileChannels(pid)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "profile not running or channels not loaded"})
			return
		}

		filtered := make([]channelInfo, 0, lim)
		total := 0
		// First pass: collect all matching channels with their index
		type channelMatch struct {
			idx int
			ch  *stalker.Channel
		}
		matches := make([]channelMatch, 0)
		for idx, title := range keys {
			ch := chs[title]
			if ch == nil {
				continue
			}
			if genreID != "" {
				chGID := strings.TrimSpace(ch.GenreID)
				// "Other" is a special case that matches empty/blank GenreID
				if genreID == "Other" {
					if chGID != "" && chGID != "Other" {
						continue
					}
				} else if chGID != genreID {
					continue
				}
			}
			if query != "" && !strings.Contains(strings.ToLower(title), query) {
				continue
			}
			allowed := filterstore.IsAllowed(pid, ch)
			if state == "enabled" && !allowed {
				continue
			}
			if state == "disabled" && allowed {
				continue
			}
			total++
			matches = append(matches, channelMatch{idx: idx, ch: ch})
		}

		sort.Slice(matches, func(i, j int) bool {
			ti := matches[i].ch.Title
			tj := matches[j].ch.Title
			if ti == "" {
				ti = keys[matches[i].idx]
			}
			if tj == "" {
				tj = keys[matches[j].idx]
			}
			return stalker.CompareNatural(ti, tj) < 0
		})

		// Only get channels for the current page
		pageMatches := make([]channelMatch, 0, lim)
		for i, m := range matches {
			if i < off {
				continue
			}
			if len(pageMatches) >= lim {
				break
			}
			pageMatches = append(pageMatches, m)
		}
		
		// Build result
		for _, m := range pageMatches {
			ch := m.ch
			title := ch.Title
			if title == "" {
				title = "Unknown"
			}
			
			filtered = append(filtered, channelInfo{
				Title:   title,
				CMD:     ch.CMD,
				GenreID: ch.GenreID,
				Genre:   ch.Genre(),
				Enabled: filterstore.IsAllowed(pid, ch),
			})
		}

		_ = json.NewEncoder(w).Encode(channelsResp{Total: total, Items: filtered})
	})

	mux.HandleFunc("/api/filters/toggle_genre", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		pid := atoiSafe(r.FormValue("id"))
		gid := strings.TrimSpace(r.FormValue("genre_id"))
		dis := strings.TrimSpace(r.FormValue("disabled"))
		disabled := dis == "1" || strings.EqualFold(dis, "true")
		filterstore.SetGenreDisabled(pid, gid, disabled)
		_ = SaveFilters()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/filters/toggle_channel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		pid := atoiSafe(r.FormValue("id"))
		cmd := strings.TrimSpace(r.FormValue("cmd"))
		if cmd == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "missing cmd"})
			return
		}
		dis := strings.TrimSpace(r.FormValue("disabled"))
		disabled := dis == "1" || strings.EqualFold(dis, "true")
		filterstore.SetChannelDisabled(pid, cmd, disabled)
		_ = SaveFilters()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/filters/toggle_category", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		pid := atoiSafe(r.FormValue("id"))
		cat := strings.TrimSpace(r.FormValue("category"))
		if cat == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "missing category"})
			return
		}
		dis := strings.TrimSpace(r.FormValue("disabled"))
		disabled := dis == "1" || strings.EqualFold(dis, "true")

		chs, _, ok := GetProfileChannels(pid)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "profile not running or channels not loaded"})
			return
		}
		// Disable/enable by flipping all genre IDs in this derived category.
		seen := map[string]struct{}{}
		for _, ch := range chs {
			if ch == nil {
				continue
			}
			if stalker.DeriveCategory(strings.TrimSpace(ch.Genre())) != cat {
				continue
			}
			gid := strings.TrimSpace(ch.GenreID)
			if gid == "" {
				gid = "Other"
			}
			if _, ok := seen[gid]; ok {
				continue
			}
			seen[gid] = struct{}{}
			filterstore.SetGenreDisabled(pid, gid, disabled)
		}
		_ = SaveFilters()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "genres": len(seen)})
	})

	mux.HandleFunc("/api/filters/bulk_categories", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		pid := atoiSafe(r.FormValue("id"))
		dis := strings.TrimSpace(r.FormValue("disabled"))
		disabled := dis == "1" || strings.EqualFold(dis, "true")
		raw := strings.TrimSpace(r.FormValue("categories"))
		if raw == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "missing categories"})
			return
		}
		raw = strings.ReplaceAll(raw, "\r", "\n")
		raw = strings.ReplaceAll(raw, "\t", "\n")
		raw = strings.ReplaceAll(raw, ",", "\n")
		cats := map[string]struct{}{}
		for _, p := range strings.Split(raw, "\n") {
			c := strings.TrimSpace(p)
			if c == "" {
				continue
			}
			cats[c] = struct{}{}
			if len(cats) >= 5000 {
				break
			}
		}
		if len(cats) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "no valid categories"})
			return
		}

		chs, _, ok := GetProfileChannels(pid)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "profile not running or channels not loaded"})
			return
		}
		genreSet := map[string]struct{}{}
		for _, ch := range chs {
			if ch == nil {
				continue
			}
			cat := stalker.DeriveCategory(strings.TrimSpace(ch.Genre()))
			if _, ok := cats[cat]; !ok {
				continue
			}
			gid := strings.TrimSpace(ch.GenreID)
			if gid == "" {
				gid = "Other"
			}
			genreSet[gid] = struct{}{}
		}
		for gid := range genreSet {
			filterstore.SetGenreDisabled(pid, gid, disabled)
		}
		_ = SaveFilters()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "categories": len(cats), "genres": len(genreSet)})
	})

	mux.HandleFunc("/api/filters/bulk_genres", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		pid := atoiSafe(r.FormValue("id"))
		dis := strings.TrimSpace(r.FormValue("disabled"))
		disabled := dis == "1" || strings.EqualFold(dis, "true")
		raw := strings.TrimSpace(r.FormValue("genre_ids"))
		if raw == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "missing genre_ids"})
			return
		}
		raw = strings.ReplaceAll(raw, "\r", "\n")
		raw = strings.ReplaceAll(raw, "\t", "\n")
		raw = strings.ReplaceAll(raw, ",", "\n")
		seen := map[string]struct{}{}
		count := 0
		for _, p := range strings.Split(raw, "\n") {
			gid := strings.TrimSpace(p)
			if gid == "" {
				continue
			}
			if _, ok := seen[gid]; ok {
				continue
			}
			seen[gid] = struct{}{}
			filterstore.SetGenreDisabled(pid, gid, disabled)
			count++
			if count >= 100000 {
				break
			}
		}
		_ = SaveFilters()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "updated": count})
	})

	mux.HandleFunc("/api/filters/bulk_channels", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		pid := atoiSafe(r.FormValue("id"))
		dis := strings.TrimSpace(r.FormValue("disabled"))
		disabled := dis == "1" || strings.EqualFold(dis, "true")
		raw := strings.TrimSpace(r.FormValue("cmds"))
		if raw == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "missing cmds"})
			return
		}
		// Accept comma, newline, or space separated CMDs.
		raw = strings.ReplaceAll(raw, "\r", "\n")
		raw = strings.ReplaceAll(raw, "\t", "\n")
		raw = strings.ReplaceAll(raw, ",", "\n")
		parts := strings.Split(raw, "\n")
		count := 0
		seen := map[string]struct{}{}
		for _, p := range parts {
			cmd := strings.TrimSpace(p)
			if cmd == "" {
				continue
			}
			if _, ok := seen[cmd]; ok {
				continue
			}
			seen[cmd] = struct{}{}
			filterstore.SetChannelDisabled(pid, cmd, disabled)
			count++
			// Hard safety cap so one request can't accidentally jam memory.
			if count >= 100000 {
				break
			}
		}
		_ = SaveFilters()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "updated": count})
	})

	mux.HandleFunc("/api/filters/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		pid := atoiSafe(r.FormValue("id"))
		filterstore.ResetProfile(pid)
		_ = SaveFilters()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
}

// helper to ensure we keep imports referenced
var _ = stalker.Channel{}
