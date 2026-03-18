package hls

import (
	"context"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/kidpoleon/stalkerhek/stalker"
)

type serverState struct {
	profileID      int
	playlist       map[string]*Channel
	sortedChannels []string
}

// Start starts main routine.
func Start(profileID int, chs map[string]*stalker.Channel, bind string) {
	StartWithContext(context.Background(), profileID, chs, bind)
}

// StartWithContext starts main routine with graceful shutdown support.
func StartWithContext(ctx context.Context, profileID int, chs map[string]*stalker.Channel, bind string) {
	// Initialize playlist
	s := &serverState{profileID: profileID, playlist: make(map[string]*Channel), sortedChannels: make([]string, 0, len(chs))}
	for k, v := range chs {
		s.playlist[k] = &Channel{
			StalkerChannel: v,
			Mux:            &sync.Mutex{},
			Logo: &Logo{
				Mux:  &sync.Mutex{},
				Link: v.Logo(),
			},
			Genre:    v.Genre(),
			RawGenre: v.RawGenre(),
		}
		s.sortedChannels = append(s.sortedChannels, k)
	}
	sort.Strings(s.sortedChannels)

	mux := http.NewServeMux()
	mux.HandleFunc("/iptv", s.playlistHandler)
	mux.HandleFunc("/iptv/", s.channelHandler)
	mux.HandleFunc("/logo/", s.logoHandler)
	// Root endpoints: playlist at "/" and channels at "/<title>".
	mux.HandleFunc("/", s.rootHandler)

	server := &http.Server{
		Addr:    bind,
		Handler: mux,
	}

	log.Println("HLS service should be started!")

	// Start server in goroutine
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HLS server error: %v", err)
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()
	log.Println("HLS shutdown: draining new requests for 3 seconds...")
	time.Sleep(3 * time.Second)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HLS server shutdown error: %v", err)
	} else {
		log.Println("HLS server shutdown complete")
	}
}
