package webui

import (
	"sort"
	"strings"
	"sync"

	"github.com/kidpoleon/stalkerhek/stalker"
)

var (
	chMu        sync.RWMutex
	profileChs  = map[int]map[string]*stalker.Channel{}
	profileKeys = map[int][]string{}
)

func SetProfileChannels(profileID int, chs map[string]*stalker.Channel) {
	chMu.Lock()
	defer chMu.Unlock()
	profileChs[profileID] = chs
	keys := make([]string, 0, len(chs))
	for k := range chs {
		if strings.TrimSpace(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return stalker.CompareNatural(keys[i], keys[j]) < 0
	})
	profileKeys[profileID] = keys
}

func ClearProfileChannels(profileID int) {
	chMu.Lock()
	defer chMu.Unlock()
	delete(profileChs, profileID)
	delete(profileKeys, profileID)
}

func GetProfileChannels(profileID int) (map[string]*stalker.Channel, []string, bool) {
	chMu.RLock()
	defer chMu.RUnlock()
	chs, ok := profileChs[profileID]
	if !ok || chs == nil {
		return nil, nil, false
	}
	keys := profileKeys[profileID]
	outKeys := make([]string, len(keys))
	copy(outKeys, keys)
	return chs, outKeys, true
}
