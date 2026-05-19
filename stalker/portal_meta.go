package stalker

import (
	"encoding/json"
	"net/url"
	"path"
	"strings"
)

// PortalMeta is optional data from get_profile (EPG URL, timezone hints).
type PortalMeta struct {
	EPGURL   string
	TimeZone string
}

// FetchPortalMeta loads profile metadata from the portal after authentication.
func (p *Portal) FetchPortalMeta() (*PortalMeta, error) {
	if p == nil {
		return nil, nil
	}
	link := p.Location + "?type=stb&action=get_profile&JsHttpRequest=1-xml&hd=1&sn=" +
		url.QueryEscape(p.SerialNumber) + "&stb_type=" + url.QueryEscape(p.Model) +
		"&device_id=" + url.QueryEscape(p.DeviceID) + "&device_id2=" + url.QueryEscape(p.DeviceID2) +
		"&auth_second_step=1"
	content, err := p.httpRequest(link)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Js map[string]interface{} `json:"js"`
	}
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil, err
	}
	meta := &PortalMeta{}
	for _, key := range []string{"epg_url", "xmltv_url", "epg_path", "epg_link"} {
		if u := p.metaEPGURL(raw.Js, key); u != "" {
			meta.EPGURL = u
			break
		}
	}
	if tz := metaString(raw.Js, "timezone", "time_zone"); tz != "" {
		meta.TimeZone = tz
	}
	return meta, nil
}

func (p *Portal) metaEPGURL(m map[string]interface{}, key string) string {
	s := metaString(m, key)
	if s == "" {
		return ""
	}
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return s
	}
	u, err := url.Parse(strings.TrimSpace(p.Location))
	if err != nil {
		return ""
	}
	if strings.HasPrefix(s, "/") {
		u.Path = s
	} else {
		u.Path = path.Join(path.Dir(u.Path), s)
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func metaString(m map[string]interface{}, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		s := strings.TrimSpace(stringFromJSON(v))
		if s != "" {
			return s
		}
	}
	return ""
}

func stringFromJSON(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		b, _ := json.Marshal(v)
		return strings.Trim(strings.TrimSpace(string(b)), `"`)
	}
}

// ResolveEPGURL picks custom profile URL, else portal-embedded guide.
func ResolveEPGURL(custom string, meta *PortalMeta) string {
	if u := strings.TrimSpace(custom); u != "" {
		return u
	}
	if meta != nil {
		return strings.TrimSpace(meta.EPGURL)
	}
	return ""
}
