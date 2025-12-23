package stalker

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"net/http"
)

// Handshake reserves a offered token in Portal. If offered token is not available - new one will be issued by stalker portal, reservedMAG254 and Stalker's config will be updated.
func (p *Portal) handshake() error {
	// This HTTP request has different headers from the rest of HTTP requests, so perform it manually
	type tmpStruct struct {
		Js map[string]interface{} `json:"js"`
	}
	var tmp tmpStruct

	req, err := http.NewRequest("GET", p.Location+"?type=stb&action=handshake&token="+p.Token+"&JsHttpRequest=1-xml", nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG200 stbapp ver: 4 rev: 2116 Mobile Safari/533.3")
	req.Header.Set("X-User-Agent", "Model: "+p.Model+"; Link: Ethernet")
	req.Header.Set("Cookie", "sn="+p.SerialNumber+"; mac="+p.MAC+"; stb_lang=en; timezone="+p.TimeZone)

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	contents, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if err = json.Unmarshal(contents, &tmp); err != nil {
		log.Println(string(contents))
		return err
	}

	token, ok := tmp.Js["token"]
	if !ok || token == "" {
		// Token accepted. Using accepted token
		return nil
	}
	// Server provided new token. Using new provided token
	p.Token = token.(string)
	return nil
}

// Authenticate associates credentials with token. In other words - logs you in
func (p *Portal) authenticate() (err error) {
	// This HTTP request has different headers from the rest of HTTP requests, so perform it manually
	type tmpStruct struct {
		Js   bool   `json:"js"`
		Text string `json:"text"`
	}
	var tmp tmpStruct

	content, err := p.httpRequest(p.Location + "?type=stb&action=do_auth&login=" + p.Username + "&password=" + p.Password + "&device_id=" + p.DeviceID + "&device_id2=" + p.DeviceID2 + "&JsHttpRequest=1-xml")
	if err != nil {
		log.Println("HTTP authentication request failed")
		return err
	}

	if err = json.Unmarshal(content, &tmp); err != nil {
		log.Println("parsing authentication response failed")
		return err
	}

	log.Println("Logging in to Stalker says:")
	log.Println(tmp.Text)

	if tmp.Js {
		// all good
		return nil
	}

	// questionable, but probably bad credentials
	return errors.New("invalid credentials")
}

// Authenticate with Device IDs
func (p *Portal) authenticateWithDeviceIDs() (err error) {
	// This HTTP request has different headers from the rest of HTTP requests, so perform it manually
	type tmpStruct struct {
		Js struct {
			Id    string `json:"id"`
			Fname string `json:"fname"`
		} `json:"js"`
		Text string `json:"text"`
	}
	var tmp tmpStruct

	log.Println("Authenticating with DeviceId and DeviceId2")
	content, err := p.httpRequest(p.Location + "?type=stb&action=get_profile&JsHttpRequest=1-xml&hd=1&sn=" + p.SerialNumber + "&stb_type=" + p.Model + "&device_id=" + p.DeviceID + "&device_id2=" + p.DeviceID2 + "&auth_second_step=1")

	if err != nil {
		log.Println("HTTP authentication request failed")
		return err
	}

	if err = json.Unmarshal(content, &tmp); err != nil {
		log.Println("Unexpected authentication response")
		return err
	}

	log.Println("Logging in to Stalker says:")
	log.Println(tmp.Text)

	if tmp.Js.Id != "" {
		log.Println("Authenticated as " + tmp.Js.Fname)
		return nil
	}

	// questionable, but probably bad credentials
	return errors.New("invalid credentials")
}
