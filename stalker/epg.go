package stalker

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// EPGProgram is a normalized EPG entry from the portal.
type EPGProgram struct {
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Category    string    `json:"category,omitempty"`
	Start       time.Time `json:"start"`
	Stop        time.Time `json:"stop"`
}

// GetShortEPG fetches upcoming programs for a live channel via Stalker get_short_epg.
func (c *Channel) GetShortEPG(size int) ([]EPGProgram, error) {
	if c == nil || c.Portal == nil {
		return nil, fmt.Errorf("channel or portal is nil")
	}
	chID := c.EPGChannelID()
	if chID == "" {
		return nil, fmt.Errorf("channel has no EPG id")
	}
	if size <= 0 {
		size = 4
	}
	if size > 20 {
		size = 20
	}
	link := c.Portal.Location + "?type=itv&action=get_short_epg&JsHttpRequest=1-xml&ch_id=" +
		url.QueryEscape(chID) + "&size=" + strconv.Itoa(size)
	content, err := c.Portal.httpRequest(link)
	if err != nil {
		return nil, err
	}
	return c.parseEPGResponse(content)
}

// GetEPGInfo fetches EPG via get_epg_info (fallback when get_short_epg is empty).
func (c *Channel) GetEPGInfo() ([]EPGProgram, error) {
	if c == nil || c.Portal == nil {
		return nil, fmt.Errorf("channel or portal is nil")
	}
	chID := c.EPGChannelID()
	if chID == "" {
		return nil, fmt.Errorf("channel has no EPG id")
	}
	link := c.Portal.Location + "?type=itv&action=get_epg_info&JsHttpRequest=1-xml&ch_id=" + url.QueryEscape(chID)
	content, err := c.Portal.httpRequest(link)
	if err != nil {
		return nil, err
	}
	return c.parseEPGResponse(content)
}

func (c *Channel) parseEPGResponse(content []byte) ([]EPGProgram, error) {
	loc := c.epgLocation()
	type tmpStruct struct {
		Js json.RawMessage `json:"js"`
	}
	var tmp tmpStruct
	if err := json.Unmarshal(content, &tmp); err != nil {
		return nil, fmt.Errorf("epg: invalid response: %w", err)
	}
	rawItems := extractEPGItems(tmp.Js)
	out := make([]EPGProgram, 0, len(rawItems))
	for _, it := range rawItems {
		p, ok := normalizeEPGItemIn(loc, it)
		if ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func parseEPGResponse(content []byte) ([]EPGProgram, error) {
	type tmpStruct struct {
		Js json.RawMessage `json:"js"`
	}
	var tmp tmpStruct
	if err := json.Unmarshal(content, &tmp); err != nil {
		return nil, fmt.Errorf("epg: invalid response: %w", err)
	}
	rawItems := extractEPGItems(tmp.Js)
	out := make([]EPGProgram, 0, len(rawItems))
	for _, it := range rawItems {
		p, ok := normalizeEPGItemIn(time.UTC, it)
		if ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func extractEPGItems(js json.RawMessage) []map[string]interface{} {
	if len(js) == 0 {
		return nil
	}
	var list []map[string]interface{}
	if err := json.Unmarshal(js, &list); err == nil {
		return list
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(js, &obj); err != nil {
		return nil
	}
	for _, key := range []string{"epg", "data"} {
		if v, ok := obj[key].([]interface{}); ok {
			for _, el := range v {
				if m, ok := el.(map[string]interface{}); ok {
					list = append(list, m)
				}
			}
			return list
		}
	}
	return nil
}

func normalizeEPGItemIn(loc *time.Location, it map[string]interface{}) (EPGProgram, bool) {
	title := epgFirstString(it, "name", "title", "progname", "program")
	start := epgTimeIn(loc, it, "start", "start_timestamp", "from", "time")
	stop := epgTimeIn(loc, it, "end", "stop_timestamp", "to", "time_to")
	if title == "" {
		title = "—"
	}
	if start.IsZero() && stop.IsZero() {
		return EPGProgram{}, false
	}
	desc := epgFirstString(it, "descr", "description", "desc", "short_description", "plot")
	cat := epgFirstString(it, "category", "genre", "categories")
	return EPGProgram{
		Title:       title,
		Description: desc,
		Category:    cat,
		Start:       start,
		Stop:        stop,
	}, true
}

func epgFirstString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			s := strings.TrimSpace(fmt.Sprint(v))
			if s != "" {
				return s
			}
		}
	}
	return ""
}

func (c *Channel) epgLocation() *time.Location {
	if c != nil && c.Portal != nil {
		return c.Portal.TimeLocation()
	}
	return time.UTC
}

func epgTimeIn(loc *time.Location, m map[string]interface{}, keys ...string) time.Time {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case float64:
			return epochInZone(int64(t), loc)
		case json.Number:
			if i, err := t.Int64(); err == nil {
				return epochInZone(i, loc)
			}
		case string:
			s := strings.TrimSpace(t)
			if s == "" {
				continue
			}
			if loc == nil {
				loc = time.UTC
			}
			for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339} {
				if dt, err := time.ParseInLocation(layout, s, loc); err == nil {
					return dt
				}
			}
		}
	}
	return time.Time{}
}

func epgTime(m map[string]interface{}, keys ...string) time.Time {
	return epgTimeIn(time.UTC, m, keys...)
}
