package maintenance

import (
	"context"
	"log"
	"os"
	"time"
)

const (
	defaultRestartInterval = 24 * time.Hour
	envRestartHours        = "STALKERHEK_RESTART_HOURS"
)

// StartScheduledRestart exits the process on a fixed interval so Docker (or a
// process supervisor) can restart the service. Profiles, auth, and filters on
// disk are untouched — only in-memory state (connections, caches, logs) reset.
func StartScheduledRestart(ctx context.Context) {
	interval := defaultRestartInterval
	if v := os.Getenv(envRestartHours); v != "" {
		if h, err := time.ParseDuration(v + "h"); err == nil && h > 0 {
			interval = h
		}
	}

	log.Printf("[MAINTENANCE] Scheduled service recycle every %s (config on disk is preserved)", interval)

	go func() {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				log.Println("[MAINTENANCE] 24h service recycle — stopping process for clean restart (credentials unchanged)")
				// Brief grace so log lines flush.
				time.Sleep(500 * time.Millisecond)
				os.Exit(0)
			}
		}
	}()
}
