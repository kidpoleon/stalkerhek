package stalker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// VODCategory is a movies/VOD grouping from the portal.
type VODCategory struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// VODItem is a movie or series entry from get_ordered_list.
type VODItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	CMD      string `json:"cmd,omitempty"`
	Logo     string `json:"logo,omitempty"`
	IsSeries string `json:"is_series,omitempty"`
	Category string `json:"category_id,omitempty"`
}

// GetVODCategories returns VOD categories (movies) from the portal.
func (p *Portal) GetVODCategories() ([]VODCategory, error) {
	link := p.Location + "?type=vod&action=get_categories&JsHttpRequest=1-xml"
	content, err := p.httpRequest(link)
	if err != nil {
		return nil, err
	}
	type tmpStruct struct {
		Js []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Alias string `json:"alias"`
		} `json:"js"`
	}
	var tmp tmpStruct
	if err := json.Unmarshal(content, &tmp); err != nil {
		return nil, fmt.Errorf("get_categories(vod): invalid response: %w", err)
	}
	out := make([]VODCategory, 0, len(tmp.Js))
	for _, c := range tmp.Js {
		title := strings.TrimSpace(c.Title)
		if title == "" {
			title = strings.TrimSpace(c.Alias)
		}
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		out = append(out, VODCategory{ID: id, Title: title})
	}
	return out, nil
}

// GetSeriesCategories returns series categories when the portal exposes them separately.
func (p *Portal) GetSeriesCategories() ([]VODCategory, error) {
	link := p.Location + "?type=series&action=get_categories&JsHttpRequest=1-xml"
	content, err := p.httpRequest(link)
	if err != nil {
		return nil, err
	}
	type tmpStruct struct {
		Js []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"js"`
	}
	var tmp tmpStruct
	if err := json.Unmarshal(content, &tmp); err != nil {
		return nil, fmt.Errorf("get_categories(series): invalid response: %w", err)
	}
	out := make([]VODCategory, 0, len(tmp.Js))
	for _, c := range tmp.Js {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		out = append(out, VODCategory{ID: id, Title: strings.TrimSpace(c.Title)})
	}
	return out, nil
}

// GetVODList fetches a page of VOD items for a category (0-based page).
func (p *Portal) GetVODList(categoryID string, page int) ([]VODItem, int, error) {
	if page < 0 {
		page = 0
	}
	q := url.Values{}
	q.Set("type", "vod")
	q.Set("action", "get_ordered_list")
	q.Set("JsHttpRequest", "1-xml")
	q.Set("category", categoryID)
	q.Set("p", strconv.Itoa(page))
	link := p.Location + "?" + q.Encode()
	content, err := p.httpRequest(link)
	if err != nil {
		return nil, 0, err
	}
	return parseVODList(content)
}

func parseVODList(content []byte) ([]VODItem, int, error) {
	type tmpStruct struct {
		Js json.RawMessage `json:"js"`
	}
	var tmp tmpStruct
	if err := json.Unmarshal(content, &tmp); err != nil {
		return nil, 0, fmt.Errorf("vod list: invalid response: %w", err)
	}
	js := bytes.TrimSpace(tmp.Js)
	if len(js) == 0 {
		return nil, 0, nil
	}
	type jsPayload struct {
		TotalItems int `json:"total_items"`
		Data       []struct {
			ID       interface{} `json:"id"`
			Name     string      `json:"name"`
			CMD      string      `json:"cmd"`
			Logo     string      `json:"screenshot_uri"`
			IsSeries string      `json:"is_series"`
		} `json:"data"`
	}
	var payload jsPayload
	if err := json.Unmarshal(js, &payload); err != nil {
		return nil, 0, fmt.Errorf("vod list: invalid js payload: %w", err)
	}
	out := make([]VODItem, 0, len(payload.Data))
	for _, row := range payload.Data {
		id := strings.TrimSpace(fmt.Sprint(row.ID))
		if id == "" || id == "<nil>" {
			id = strings.TrimSpace(row.CMD)
		}
		name := strings.TrimSpace(row.Name)
		if name == "" {
			continue
		}
		if strings.TrimSpace(row.IsSeries) == "1" {
			continue
		}
		out = append(out, VODItem{
			ID:       id,
			Name:     name,
			CMD:      strings.TrimSpace(row.CMD),
			Logo:     strings.TrimSpace(row.Logo),
			IsSeries: strings.TrimSpace(row.IsSeries),
		})
	}
	return out, payload.TotalItems, nil
}

// NewVODLink creates a playable URL for a VOD item (movie).
func (p *Portal) NewVODLink(cmd string) (string, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", fmt.Errorf("empty vod cmd")
	}
	if !strings.HasPrefix(cmd, "/") && !strings.HasPrefix(strings.ToLower(cmd), "ffmpeg") {
		cmd = "/media/file_" + cmd + ".mpg"
	}
	link := p.Location + "?action=create_link&type=vod&cmd=" + url.PathEscape(cmd) + "&JsHttpRequest=1-xml"
	type tmpStruct struct {
		Js struct {
			Cmd string `json:"cmd"`
			URL string `json:"url"`
		} `json:"js"`
	}
	var tmp tmpStruct
	content, err := p.httpRequest(link)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(content, &tmp); err != nil {
		return "", fmt.Errorf("create_link(vod): invalid response: %w", err)
	}
	if u := strings.TrimSpace(tmp.Js.URL); u != "" {
		return u, nil
	}
	raw := strings.TrimSpace(tmp.Js.Cmd)
	if raw == "" {
		return "", fmt.Errorf("empty create_link(vod) response")
	}
	return extractStreamURL(raw), nil
}

func extractStreamURL(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(strings.ToLower(cmd), "ffmpeg ") {
		cmd = strings.TrimSpace(cmd[6:])
	}
	if strings.HasPrefix(strings.ToLower(cmd), "ffmpeg") {
		cmd = strings.TrimSpace(cmd[6:])
	}
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
