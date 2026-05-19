package hls

import (
	"fmt"
	"log"
	"net/http"
	"net/url"

	"github.com/kidpoleon/stalkerhek/stalker"
)

// writeExtInf writes a single #EXTINF line and stream URL to the playlist.
//
// Field policy (keeps M3U8 clean for tools like Dispatcharr):
//   - tvg-id    : blank — the raw Stalker CMD is an ffmpeg URL, not an EPG ID
//   - tvg-name  : channel title with superscript/subscript decorators stripped
//   - tvg-logo  : blank — relative logo paths break external tools; logo
//                 serving still works internally via the /logo/ endpoint
//   - group-title: raw portal genre name with superscripts stripped; this is
//                  the "US| ESPN" level that sits directly above channels in
//                  the UI drill-down, which is what M3U8 players group by
func writeExtInf(w http.ResponseWriter, title, rawGenre, link string) {
	tvgName := escapeM3U8Attr(stalker.CleanTitleForM3U8(title))
	groupTitle := escapeM3U8Attr(stalker.CleanGenreForM3U8(rawGenre))
	displayName := escapeM3U8Attr(stalker.CleanTitleForM3U8(title))
	fmt.Fprintf(w,
		"#EXTINF:-1 tvg-id=\"\" tvg-name=\"%s\" tvg-logo=\"\" group-title=\"%s\", %s\n%s\n",
		tvgName, groupTitle, displayName, link,
	)
}

// writePlaylist writes all #EXTINF entries sorted by group-title then channel
// name (natural order). Duplicate cleaned titles keep the first entry after sort.
func (s *serverState) writePlaylist(w http.ResponseWriter, scheme, host, prefix string) {
	items := buildSortedPlaylistItems(s.profileID, s.sortedChannels, s.playlist)
	seen := make(map[string]struct{}, len(items))
	for _, it := range items {
		if it.cleanName == "" {
			continue
		}
		if _, dup := seen[it.cleanName]; dup {
			continue
		}
		seen[it.cleanName] = struct{}{}
		link := scheme + "://" + host + prefix + url.PathEscape(it.title)
		writeExtInf(w, it.title, it.ch.RawGenre, link)
	}
}

// Handles '/iptv' requests
func (s *serverState) playlistHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	scheme, host := externalBase(r)
	fmt.Fprintln(w, "#EXTM3U")
	s.writePlaylist(w, scheme, host, "/iptv/")
}

// Handles '/iptv/' requests
func (s *serverState) channelHandler(w http.ResponseWriter, r *http.Request) {
	cr, err := s.getContentRequest(w, r, "/iptv/")
	if err != nil {
		if err == errForbidden {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	// Lock channel's mux for validation only
	cr.ChannelRef.Mux.Lock()

	// Keep track on channel access time
	if err = cr.ChannelRef.validate(); err != nil {
		log.Printf("[ERROR] Channel validation failed for %s: %v", cr.Title, err)
		cr.ChannelRef.Mux.Unlock()
		http.Error(w, "channel unavailable", http.StatusServiceUnavailable)
		return
	}

	// handleContent copies channel state and releases the lock before streaming.
	handleContent(cr)
}

// Handles '/logo/' requests
func (s *serverState) logoHandler(w http.ResponseWriter, r *http.Request) {
	cr, err := s.getContentRequest(w, r, "/logo/")
	if err != nil {
		if err == errForbidden {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	// Lock
	cr.ChannelRef.Logo.Mux.Lock()

	// Retrieve from Stalker middleware if no cache is present
	if len(cr.ChannelRef.Logo.Cache) == 0 {
		img, contentType, err := download(cr.ChannelRef.Logo.Link)
		if err != nil {
			cr.ChannelRef.Logo.Mux.Unlock()
			http.Error(w, "internal server error", http.StatusInternalServerError)
			log.Println(err)
			return
		}
		cr.ChannelRef.Logo.Cache = img
		cr.ChannelRef.Logo.CacheContentType = contentType
	}

	// Create local copy so we don't need thread synchronization
	logo := *cr.ChannelRef.Logo

	// Unlock
	cr.ChannelRef.Logo.Mux.Unlock()

	w.Header().Set("Content-Type", logo.CacheContentType)
	w.Write(logo.Cache)
}

// rootHandler serves playlist at "/" and channels at root paths without the "/iptv" prefix.
func (s *serverState) rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		// Serve playlist at root
		w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		scheme, host := externalBase(r)

		fmt.Fprintln(w, "#EXTM3U")
		s.writePlaylist(w, scheme, host, "/")
		return
	}

	// Treat anything else at root as a channel request
	cr, err := s.getContentRequest(w, r, "/")
	if err != nil {
		if err == errForbidden {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	// Lock channel's mux
	cr.ChannelRef.Mux.Lock()

	// Keep track on channel access time
	if err = cr.ChannelRef.validate(); err != nil {
		cr.ChannelRef.Mux.Unlock()
		http.Error(w, "channel unavailable", http.StatusServiceUnavailable)
		log.Printf("[HLS] channel validation failed channel=%q err=%v", cr.Title, err)
		return
	}

	handleContent(cr)
}
