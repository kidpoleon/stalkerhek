package webui

// streamwatch.go — lightweight per-profile stream watchdog.
//
// Probes each running profile's HLS/Proxy port via a plain TCP dial
// to localhost — completely invisible to the upstream portal. Only
// after WATCHDOG_MIN_FAILURES consecutive missed probes does it
// attempt revival via StartProfileServices (which hits the portal).
//
// Only one environment variable is exposed:
//
//   WATCHDOG_MAX_ATTEMPTS   integer   (default: 5)
//     How many revival attempts to make before giving up on a profile.
//     Set to 0 to disable the watchdog entirely.

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	wdStartupGrace = 120 * time.Second // wait after process start before any probing
	wdPollInterval = 60 * time.Second  // how often to probe all profiles
	wdProbeTimeout = 3 * time.Second   // TCP dial timeout per probe
	wdBaseDelay    = 15 * time.Second  // first backoff delay before revival attempt 1
	wdMaxDelay     = 5 * time.Minute   // backoff ceiling
	wdMinFailures  = 2                 // consecutive missed probes required before acting
	wdDefaultMax   = 5                 // default WATCHDOG_MAX_ATTEMPTS
)

func watchdogMaxAttempts() int {
	v := strings.TrimSpace(os.Getenv("WATCHDOG_MAX_ATTEMPTS"))
	if v == "" {
		return wdDefaultMax
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Printf("[watchdog] bad WATCHDOG_MAX_ATTEMPTS=%q, using default %d", v, wdDefaultMax)
		return wdDefaultMax
	}
	return n
}

// wdProfileState is tracked per profile ID by the watchdog.
type wdProfileState struct {
	consecutiveMisses int  // probes failed in a row; reset on any healthy probe
	revivalAttempts   int  // revival calls made since last clean probe
	gaveUp            bool // true once max revival attempts are exhausted
}

var (
	wdMu     sync.Mutex
	wdStates = map[int]*wdProfileState{}
)

func wdGetState(id int) *wdProfileState {
	// must be called with wdMu held
	s := wdStates[id]
	if s == nil {
		s = &wdProfileState{}
		wdStates[id] = s
	}
	return s
}

func wdReset(id int) {
	wdMu.Lock()
	delete(wdStates, id)
	wdMu.Unlock()
}

// StartStreamWatchdog starts the background watchdog goroutine.
// Call once from main after profiles are loaded.
func StartStreamWatchdog(ctx context.Context) {
	maxAttempts := watchdogMaxAttempts()
	if maxAttempts == 0 {
		log.Println("[watchdog] disabled (WATCHDOG_MAX_ATTEMPTS=0)")
		return
	}
	log.Printf("[watchdog] starting — grace=%s poll=%s min_failures=%d base_delay=%s max_delay=%s max_attempts=%d",
		wdStartupGrace, wdPollInterval, wdMinFailures, wdBaseDelay, wdMaxDelay, maxAttempts)
	go watchdogLoop(ctx, maxAttempts)
}

func watchdogLoop(ctx context.Context, maxAttempts int) {
	// Startup grace: services are still binding ports, portals are being
	// authenticated. Don't probe anything yet.
	select {
	case <-ctx.Done():
		return
	case <-time.After(wdStartupGrace):
	}

	ticker := time.NewTicker(wdPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[watchdog] stopped")
			return
		case <-ticker.C:
			checkAllProfiles(ctx, maxAttempts)
		}
	}
}

func checkAllProfiles(ctx context.Context, maxAttempts int) {
	for _, p := range ListProfiles() {
		if ctx.Err() != nil {
			return
		}
		if p.HlsPort == 0 && p.ProxyPort == 0 {
			continue
		}

		// Profile intentionally stopped by the user — clear state and move on.
		if !IsRunning(p.ID) {
			wdReset(p.ID)
			continue
		}

		wdMu.Lock()
		s := wdGetState(p.ID)

		if s.gaveUp {
			wdMu.Unlock()
			continue
		}

		alive := probeProfile(p)

		if alive {
			if s.consecutiveMisses > 0 || s.revivalAttempts > 0 {
				log.Printf("[watchdog] profile %d (%s): healthy again", p.ID, p.Name)
			}
			delete(wdStates, p.ID)
			wdMu.Unlock()
			continue
		}

		s.consecutiveMisses++
		misses := s.consecutiveMisses
		wdMu.Unlock()

		// Not enough consecutive failures yet — wait for confirmation before
		// doing anything that touches the upstream portal.
		if misses < wdMinFailures {
			log.Printf("[watchdog] profile %d (%s): probe miss %d/%d (waiting for confirmation)",
				p.ID, p.Name, misses, wdMinFailures)
			continue
		}

		// Confirmed dead across multiple poll cycles. Attempt revival.
		wdMu.Lock()
		s = wdGetState(p.ID)
		s.revivalAttempts++
		attempt := s.revivalAttempts
		if attempt > maxAttempts {
			if !s.gaveUp {
				s.gaveUp = true
				wdMu.Unlock()
				log.Printf("[watchdog] profile %d (%s): gave up after %d revival attempts",
					p.ID, p.Name, maxAttempts)
				AppendProfileLog(p.ID, fmt.Sprintf(
					"[watchdog] gave up after %d revival attempts — restart manually", maxAttempts))
			} else {
				wdMu.Unlock()
			}
			continue
		}
		wdMu.Unlock()

		delay := wdBackoff(attempt - 1)
		log.Printf("[watchdog] profile %d (%s): stream unreachable (%d consecutive misses) — revival attempt %d/%d after %s",
			p.ID, p.Name, misses, attempt, maxAttempts, delay)
		AppendProfileLog(p.ID, fmt.Sprintf(
			"[watchdog] stream unreachable, revival attempt %d/%d (waiting %s)",
			attempt, maxAttempts, delay))

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		if ctx.Err() != nil {
			return
		}

		// Re-check: user may have manually restarted during the backoff sleep.
		if !IsRunning(p.ID) {
			wdReset(p.ID)
			continue
		}

		log.Printf("[watchdog] profile %d (%s): triggering revival", p.ID, p.Name)
		AppendProfileLog(p.ID, "[watchdog] triggering service revival")
		go StartProfileServices(p)
	}
}

// probeProfile returns true if at least one configured port answers a TCP dial
// on localhost. Never contacts the upstream portal.
func probeProfile(p Profile) bool {
	if p.HlsPort > 0 && tcpReachable(p.HlsPort) {
		return true
	}
	if p.ProxyPort > 0 && tcpReachable(p.ProxyPort) {
		return true
	}
	return false
}

func tcpReachable(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), wdProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// wdBackoff returns baseDelay * 2^attempt, capped at wdMaxDelay.
func wdBackoff(attempt int) time.Duration {
	d := wdBaseDelay
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= wdMaxDelay {
			return wdMaxDelay
		}
	}
	return d
}
