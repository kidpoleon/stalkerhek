package stalker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"path"
	"strings"
	"time"
)

// Channel stores information about channel in Stalker portal. This is not a real TV channel representation, but details on how to retrieve a working channel's URL.
type Channel struct {
	Title    string             // Used for Proxy service to generate fake response to new URL request
	CMD      string             // channel's identifier in Stalker portal
	LogoLink string             // Link to logo
	Portal   *Portal            // Reference to portal from where this channel is taken from
	GenreID  string             // Stores genre ID (category ID)
	Genres   *map[string]string // Stores mappings for genre ID -> genre title

	CMD_ID    string // Used for Proxy service to generate fake response to new URL request
	CMD_CH_ID string // Used for Proxy service to generate fake response to new URL request
}

// NewLink retrieves a link to the working channel. Retrieved link can be played in VLC or Kodi, but expires very soon if not being constantly opened (used).
func (c *Channel) NewLink(retry bool) (string, error) {
	type tmpStruct struct {
		Js struct {
			Cmd string `json:"cmd"`
		} `json:"js"`
	}
	var tmp tmpStruct

	link := c.Portal.Location + "?action=create_link&type=itv&cmd=" + url.PathEscape(c.CMD) + "&JsHttpRequest=1-xml"

	// Use retry logic for link retrieval
	retryConfig := RetryConfig{
		MaxRetries: 3,
		BaseDelay:  1 * time.Second,
		MaxDelay:   10 * time.Second,
	}

	var content []byte
	err := RetryWithBackoff(retryConfig, func() error {
		var err error
		content, err = c.Portal.httpRequest(link)
		return err
	})

	if err != nil {
		return "", err
	}

	if err := json.Unmarshal(content, &tmp); err != nil {
		// It could be that session has expired and user need to authenticate again.
		log.Println("Failed to retrieve new link...")
		if !retry && c.Portal.Username != "" && c.Portal.Password != "" {
			log.Println("Attempting to re-authenticate via username and password ...")
			if err2 := c.Portal.authenticate(); err2 != nil {
				log.Println("Reauthentication failed...")
				return "", err
			}
			log.Println("Reauthentication success, retrying to retrieve new link...")
			return c.NewLink(true)
		} else if !retry && c.Portal.DeviceID != "" && c.Portal.DeviceID2 != "" {
			log.Println("Attempting to re-authenticate via Device Ids ...")
			if err2 := c.Portal.authenticateWithDeviceIDs(); err2 != nil {
				log.Println("Reauthentication failed...")
				return "", err
			}
			log.Println("Reauthentication success, retrying to retrieve new link...")
			return c.NewLink(true)
		}
		return "", err
	}

	cmd := strings.TrimSpace(tmp.Js.Cmd)
	if cmd == "" {
		return "", fmt.Errorf("empty create_link response")
	}
	strs := strings.Fields(cmd)
	if len(strs) == 0 {
		return "", fmt.Errorf("invalid create_link response")
	}
	return strs[len(strs)-1], nil
}

// Logo returns full link to channel's logo
func (c *Channel) Logo() string {
	if c.LogoLink == "" {
		return ""
	}
	base, err := url.Parse(c.Portal.Location)
	if err != nil {
		return c.Portal.Location + "misc/logos/320/" + c.LogoLink // hardcoded path - fixme?
	}
	// If Location points to portal.php, derive the directory containing it.
	base.Path = path.Dir(base.Path)
	if base.Path == "." {
		base.Path = "/"
	}
	base.RawQuery = ""
	base.Fragment = ""
	base.Path = path.Join(base.Path, "misc", "logos", "320", c.LogoLink)
	return base.String()
}

// Genre returns a genre title
func (c *Channel) Genre() string {
	if c.Genres == nil || *c.Genres == nil {
		return "Other"
	}
	g, ok := (*c.Genres)[c.GenreID]
	if !ok || strings.TrimSpace(g) == "" {
		g = "Other"
	}
	return strings.Title(g)
}

// RawGenre returns the portal genre title exactly as received, without any
// case transformation. Returns an empty string when no genre is available.
// Used for M3U8 group-title output where faithful portal casing is preferred.
func (c *Channel) RawGenre() string {
	if c.Genres == nil || *c.Genres == nil {
		return ""
	}
	g, ok := (*c.Genres)[c.GenreID]
	if !ok || strings.TrimSpace(g) == "" {
		return ""
	}
	return strings.TrimSpace(g)
}

// RetrieveChannels retrieves all TV channels from stalker portal.
// The second return value is the raw JSON response bytes for diagnostic logging.
func (p *Portal) RetrieveChannels() (map[string]*Channel, []byte, error) {
	type tmpStruct struct {
		Js json.RawMessage `json:"js"`
	}
	var tmp tmpStruct

	content, err := p.httpRequest(p.Location + "?type=itv&action=get_all_channels&JsHttpRequest=1-xml")
	if err != nil {
		return nil, nil, err
	}

	// Dump json output to file
	//ioutil.WriteFile("/tmp/dumpedchannels.json", content, 0644)

	dec := json.NewDecoder(bytes.NewReader(content))
	dec.UseNumber()
	if err := dec.Decode(&tmp); err != nil {
		return nil, nil, fmt.Errorf("get_all_channels: invalid response: %w", err)
	}
	js := bytes.TrimSpace(tmp.Js)
	if len(js) == 0 || js[0] == '[' {
		return nil, nil, fmt.Errorf("get_all_channels: portal returned no channel data (check MAC address and portal URL)")
	}
	type jsPayload struct {
		Data []struct {
			Name    string `json:"name"`
			Cmd     string `json:"cmd"`
			Logo    string `json:"logo"`
			GenreID string `json:"tv_genre_id"`
			CMDs    []struct {
				ID    string `json:"id"`
				CH_ID string `json:"ch_id"`
			} `json:"cmds"`
		} `json:"data"`
	}
	var payload jsPayload
	if err := json.Unmarshal(js, &payload); err != nil {
		return nil, nil, fmt.Errorf("get_all_channels: invalid js payload (check MAC address and portal URL)")
	}
	if len(payload.Data) == 0 {
		return nil, nil, fmt.Errorf("get_all_channels: no channels returned (check MAC address and portal URL)")
	}

	genres, err := p.getGenres()
	if err != nil {
		return nil, nil, err
	}

	// Build channels list and return
	channels := make(map[string]*Channel, len(payload.Data))
	for _, v := range payload.Data {
		var cmdID string
		var cmdCHID string
		if len(v.CMDs) > 0 {
			cmdID = v.CMDs[0].ID
			cmdCHID = v.CMDs[0].CH_ID
		}
		channels[v.Name] = &Channel{
			Title:     v.Name,
			CMD:       v.Cmd,
			LogoLink:  v.Logo,
			Portal:    p,
			GenreID:   v.GenreID,
			Genres:    &genres,
			CMD_ID:    cmdID,
			CMD_CH_ID: cmdCHID,
		}
	}

	return channels, content, nil
}

func (p *Portal) getGenres() (map[string]string, error) {
	type tmpStruct struct {
		Js []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"js"`
	}
	var tmp tmpStruct

	content, err := p.httpRequest(p.Location + "?action=get_genres&type=itv&JsHttpRequest=1-xml")
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(content, &tmp); err != nil {
		return nil, fmt.Errorf("get_genres: invalid response: %w", err)
	}

	genres := make(map[string]string, len(tmp.Js))
	for _, el := range tmp.Js {
		genres[el.ID] = el.Title
	}

	return genres, nil
}
