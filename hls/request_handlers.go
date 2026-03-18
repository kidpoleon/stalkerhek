package hls

import (
	"fmt"
	"log"
	"net/http"
	"net/url"

	"github.com/kidpoleon/stalkerhek/filterstore"
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
	tvgName := stalker.CleanTitleForM3U8(title)
	groupTitle := stalker.CleanGenreForM3U8(rawGenre)
	displayName := stalker.CleanTitleForM3U8(title)
	fmt.Fprintf(w,
		"#EXTINF:-1 tvg-id=\"\" tvg-name=\"%s\" tvg-logo=\"\" group-title=\"%s\", %s\n%s\n",
		tvgName, groupTitle, displayName, link,
	)
}

// Handles '/iptv' requests
func (s *serverState) playlistHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	scheme, host := externalBase(r)

	fmt.Fprintln(w, "#EXTM3U")
	for _, title := range s.sortedChannels {
		ch := s.playlist[title]
		if ch == nil || ch.StalkerChannel == nil {
			continue
		}
		if !filterstore.IsAllowed(s.profileID, ch.StalkerChannel) {
			continue
		}
		link := scheme + "://" + host + "/iptv/" + url.PathEscape(title)
		writeExtInf(w, title, ch.RawGenre, link)
	}
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

	// Release lock before long-running streaming to allow parallel streams
	cr.ChannelRef.Mux.Unlock()

	// Handle content without holding the lock
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
		for _, title := range s.sortedChannels {
			ch := s.playlist[title]
			if ch == nil || ch.StalkerChannel == nil {
				continue
			}
			if !filterstore.IsAllowed(s.profileID, ch.StalkerChannel) {
				continue
			}
			link := scheme + "://" + host + "/" + url.PathEscape(title)
			writeExtInf(w, title, ch.RawGenre, link)
		}
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
		http.Error(w, "internal server error", http.StatusInternalServerError)
		log.Printf("[ERROR] Channel validation failed for %s: %v", cr.Title, err)
		return
	}

	// Handle content
	handleContent(cr)
}
