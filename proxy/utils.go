package proxy

import (
	"bytes"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kidpoleon/stalkerhek/stalker"
)

const userAgent = "Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG200 stbapp ver: 4 rev: 2116 Mobile Safari/533.3"

// clientMu protects HTTPClient transport updates
var clientMu sync.RWMutex

// HTTPClient with connection pooling for proxy package
var HTTPClient = &http.Client{
	// NOTE: Do NOT set http.Client.Timeout for proxy streaming.
	// Client.Timeout covers the entire request including reading the body and will
	// terminate long-lived streams (manifesting as buffering/stalls).
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 35 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
	},
}

func UpdateResponseHeaderTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	clientMu.Lock()
	defer clientMu.Unlock()
	if tr, ok := HTTPClient.Transport.(*http.Transport); ok {
		tr.ResponseHeaderTimeout = d
	}
}

func UpdateMaxIdleConnsPerHost(n int) {
	if n <= 0 {
		return
	}
	clientMu.Lock()
	defer clientMu.Unlock()
	if tr, ok := HTTPClient.Transport.(*http.Transport); ok {
		tr.MaxIdleConnsPerHost = n
	}
}

func getRequest(link string, originalRequest *http.Request, cfg *stalker.Config) (*http.Response, error) {
	req, err := http.NewRequest("GET", link, nil)
	if err != nil {
		return nil, err
	}

	if u, err := url.Parse(link); err == nil {
		req.Header.Set("Referer", u.Scheme+"://"+u.Host+"/")
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	}

	for k, v := range originalRequest.Header {
		switch k {
		case "Authorization":
			req.Header.Set("Authorization", "Bearer "+cfg.Portal.Token)
		case "Cookie":
			cookieText := "PHPSESSID=null; sn=" + url.QueryEscape(cfg.Portal.SerialNumber) + "; mac=" + url.QueryEscape(cfg.Portal.MAC) + "; stb_lang=en; timezone=" + url.QueryEscape(cfg.Portal.TimeZone) + ";"
			req.Header.Set("Cookie", cookieText)
		case "Referer":
		case "Referrer":
		default:
			req.Header.Set(k, v[0])
		}
	}

	// Ensure required headers are present even if the downstream client did not send them.
	if req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Portal.Token)
	}
	if req.Header.Get("Cookie") == "" {
		cookieText := "PHPSESSID=null; sn=" + url.QueryEscape(cfg.Portal.SerialNumber) + "; mac=" + url.QueryEscape(cfg.Portal.MAC) + "; stb_lang=en; timezone=" + url.QueryEscape(cfg.Portal.TimeZone) + ";"
		req.Header.Set("Cookie", cookieText)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", userAgent)
	}
	if req.Header.Get("X-User-Agent") == "" {
		req.Header.Set("X-User-Agent", "Model: MAG200; Link: Ethernet")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "*/*")
	}
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	}
	if req.Header.Get("Cache-Control") == "" {
		req.Header.Set("Cache-Control", "no-cache")
	}
	if req.Header.Get("Pragma") == "" {
		req.Header.Set("Pragma", "no-cache")
	}

	clientMu.RLock()
	client := HTTPClient
	clientMu.RUnlock()

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}

	// Best-effort diagnostics: read a small prefix of the response body and then restore it.
	// This helps investigate provider blocks (e.g., HTTP 458) without breaking streaming.
	const maxDiag = 4096
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxDiag))
	resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(snippet), resp.Body))
	if len(snippet) > 0 {
		msg := strings.TrimSpace(string(snippet))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		safeLink := link
		if u, err := url.Parse(link); err == nil {
			safeLink = u.Scheme + "://" + u.Host + u.Path
		}
		log.Printf("Upstream non-2xx: %d for %s (body: %q)", resp.StatusCode, safeLink, msg)
	} else {
		safeLink := link
		if u, err := url.Parse(link); err == nil {
			safeLink = u.Scheme + "://" + u.Host + u.Path
		}
		log.Printf("Upstream non-2xx: %d for %s", resp.StatusCode, safeLink)
	}

	return resp, nil
}

func addHeaders(from, to http.Header) {
	for k, v := range from {
		to.Set(k, strings.Join(v, "; "))
	}
}

func generateNewChannelLink(link, id, ch_id string) string {
	return `{"js":{"id":"` + id + `","cmd":"` + specialLinkEscape(link) + `","streamer_id":0,"link_id":` + ch_id + `,"load":0,"error":""},"text":"array(6) {\n  [\"id\"]=>\n  string(4) \"` + id + `\"\n  [\"cmd\"]=>\n  string(99) \"` + specialLinkEscape(link) + `\"\n  [\"streamer_id\"]=>\n  int(0)\n  [\"link_id\"]=>\n  int(` + ch_id + `)\n  [\"load\"]=>\n  int(0)\n  [\"error\"]=>\n  string(0) \"\"\n}\ngenerated in: 0.01s; query counter: 8; cache hits: 0; cache miss: 0; php errors: 0; sql errors: 0;"}`
}

func specialLinkEscape(i string) string {
	return strings.ReplaceAll(i, "/", "\\/")
}
