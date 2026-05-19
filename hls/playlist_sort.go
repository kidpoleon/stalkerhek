package hls

import (
	"sort"

	"github.com/kidpoleon/stalkerhek/filterstore"
	"github.com/kidpoleon/stalkerhek/stalker"
)

type playlistItem struct {
	title     string
	ch        *Channel
	group     string
	cleanName string
}

func buildSortedPlaylistItems(profileID int, sortedTitles []string, playlist map[string]*Channel) []playlistItem {
	items := make([]playlistItem, 0, len(sortedTitles))
	for _, title := range sortedTitles {
		ch := playlist[title]
		if ch == nil || ch.StalkerChannel == nil {
			continue
		}
		if !filterstore.IsAllowed(profileID, ch.StalkerChannel) {
			continue
		}
		group := stalker.CleanGenreForM3U8(ch.RawGenre)
		if group == "" {
			group = "Other"
		}
		items = append(items, playlistItem{
			title:     title,
			ch:        ch,
			group:     group,
			cleanName: stalker.CleanTitleForM3U8(title),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		gi := stalker.CategorySortKey(a.group)
		gj := stalker.CategorySortKey(b.group)
		if gi != gj {
			return gi < gj
		}
		if a.group != b.group {
			return stalker.CompareNatural(a.group, b.group) < 0
		}
		if a.cleanName != b.cleanName {
			return stalker.CompareNatural(a.cleanName, b.cleanName) < 0
		}
		return stalker.CompareNatural(a.title, b.title) < 0
	})
	return items
}
