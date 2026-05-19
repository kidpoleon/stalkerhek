package webui

import (
	"sync"

	"github.com/kidpoleon/stalkerhek/stalker"
)

var (
	portalMu sync.RWMutex
	portals  = map[int]*stalker.Portal{}
)

func RegisterProfilePortal(profileID int, p *stalker.Portal) {
	if p == nil {
		return
	}
	portalMu.Lock()
	portals[profileID] = p
	portalMu.Unlock()
}

func ClearProfilePortal(profileID int) {
	portalMu.Lock()
	delete(portals, profileID)
	portalMu.Unlock()
}

func GetProfilePortal(profileID int) (*stalker.Portal, bool) {
	portalMu.RLock()
	p, ok := portals[profileID]
	portalMu.RUnlock()
	return p, ok
}
