package webui

import (
	"strings"
	"sync"

	"github.com/kidpoleon/stalkerhek/stalker"
)

var (
	metaMu      sync.RWMutex
	profileMeta = map[int]*stalker.PortalMeta{}
)

func SetProfileMeta(profileID int, meta *stalker.PortalMeta) {
	metaMu.Lock()
	if meta == nil {
		delete(profileMeta, profileID)
	} else {
		profileMeta[profileID] = meta
	}
	metaMu.Unlock()
}

func GetProfileMeta(profileID int) (*stalker.PortalMeta, bool) {
	metaMu.RLock()
	m, ok := profileMeta[profileID]
	metaMu.RUnlock()
	return m, ok
}

func ClearProfileMeta(profileID int) {
	metaMu.Lock()
	delete(profileMeta, profileID)
	metaMu.Unlock()
}

// EffectiveEPGURL returns profile override or portal-discovered XMLTV URL.
func EffectiveEPGURL(p Profile) string {
	if u := strings.TrimSpace(p.EPGURL); u != "" {
		return u
	}
	if m, ok := GetProfileMeta(p.ID); ok {
		return stalker.ResolveEPGURL("", m)
	}
	return ""
}
