package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/kidpoleon/stalkerhek/maintenance"
	"github.com/kidpoleon/stalkerhek/stalker"
	"github.com/kidpoleon/stalkerhek/webui"
)

var flagConfig = flag.String("config", "stalkerhek.yml", "path to the config file")

// Global context for graceful shutdown
var (
	ctx, cancel = context.WithCancel(context.Background())
	wg          sync.WaitGroup
)

func main() {
	// Change flags on the default logger, so it print's line numbers as well.
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetOutput(webui.InitInstanceLogCapture(os.Stderr))

	flag.Parse()

	webui.InitProfilesFileFromEnv()
	webui.LogInfo("STARTUP", "stalkerhek starting (runtime tuning defaults applied)")
	maintenance.StartScheduledRestart(ctx)
	if err := webui.LoadFilters(); err != nil {
		webui.LogWarn("STARTUP", "failed to load filters: %v", err)
	}

	// Initialize in-memory configuration; WebUI will collect portal URL and MAC.
	c := &stalker.Config{
		Portal: &stalker.Portal{
			Model:        "MAG254",
			SerialNumber: "0000000000000",
			DeviceID:     strings.Repeat("f", 64),
			DeviceID2:    strings.Repeat("f", 64),
			Signature:    strings.Repeat("f", 64),
			TimeZone:     "UTC",
			DeviceIdAuth: true,
			WatchDogTime: 5,
		},
	}

	// Load persisted profiles before starting anything.
	if err := webui.LoadProfiles(); err != nil {
		log.Printf("failed to load profiles: %v", err)
	}

	// Start WebUI and keep it running
	log.Println("Starting WebUI on :4400 ...")
	go webui.StartWithContext(ctx, c, make(chan struct{})) // ready ignored

	// Immediately start all saved profiles in parallel
	profiles := webui.ListProfiles()
	if len(profiles) == 0 {
		log.Println("No profiles saved yet. Add profiles via the WebUI.")
	} else {
		log.Printf("Starting %d saved profile(s) in parallel...", len(profiles))
		for _, p := range profiles {
			go webui.StartProfileServices(p) // use the new function in webui
		}
	}

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	log.Println("Shutdown signal received, stopping services...")
	cancel()
	wg.Wait()
	log.Println("All services stopped gracefully")
}
