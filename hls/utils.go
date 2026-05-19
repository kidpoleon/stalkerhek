package hls

import (
	"errors"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var playlistDelaySegments atomic.Int32

// clientMu protects httpClient transport updates
var clientMu sync.RWMutex

const userAgent = "Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG200 stbapp ver: 4 rev: 2116 Mobile Safari/533.3"

func download(link string) (content []byte, contentType string, err error) {
	resp, err := response(link)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	content, err = ioutil.ReadAll(resp.Body)
	return content, resp.Header.Get("Content-Type"), err
}

// This Golang's HTTP client will not follow redirects.
//
// This is because by default it adds "Referrer" to the header, which causes
// 404 HTTP error in some backends. With below code such header is not added
// and redirects should be performed manually.
var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   12 * time.Second,
			KeepAlive: 45 * time.Second,
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
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func SetPlaylistDelaySegments(n int) {
	if n < 0 {
		n = 0
	}
	if n > 40 {
		n = 40
	}
	playlistDelaySegments.Store(int32(n))
}

func getPlaylistDelaySegments() int {
	return int(playlistDelaySegments.Load())
}

func UpdateResponseHeaderTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	clientMu.Lock()
	defer clientMu.Unlock()
	if tr, ok := httpClient.Transport.(*http.Transport); ok {
		tr.ResponseHeaderTimeout = d
	}
}

func UpdateMaxIdleConnsPerHost(n int) {
	if n <= 0 {
		return
	}
	clientMu.Lock()
	defer clientMu.Unlock()
	if tr, ok := httpClient.Transport.(*http.Transport); ok {
		tr.MaxIdleConnsPerHost = n
	}
}

// externalBase returns the scheme and host to use for absolute URLs.
// It prefers Forwarded / X-Forwarded-* when present (typical behind reverse proxies).
func externalBase(r *http.Request) (scheme string, host string) {
	host = r.Host
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xf != "" {
		// may be a comma-separated list
		host = strings.TrimSpace(strings.Split(xf, ",")[0])
	}
	if fwd := strings.TrimSpace(r.Header.Get("Forwarded")); fwd != "" {
		// Forwarded: proto=https;host=example.com
		// Multiple Forwarded entries may be comma-separated; prefer the first.
		fwd = strings.TrimSpace(strings.Split(fwd, ",")[0])
		parts := strings.Split(fwd, ";")
		for _, p := range parts {
			kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
			if len(kv) != 2 {
				continue
			}
			k := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(kv[0]))
			v := strings.Trim(strings.TrimSpace(kv[1]), "\"")
			switch strings.ToLower(k) {
			case "proto":
				if scheme == "" {
					scheme = v
				}
			case "host":
				host = v
			}
		}
	}
	if scheme == "" {
		if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xf != "" {
			scheme = strings.TrimSpace(strings.Split(xf, ",")[0])
		}
	}
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme, host
}

func response(link string) (*http.Response, error) {
	req, err := http.NewRequest("GET", link, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-User-Agent", "Model: MAG200; Link: Ethernet")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Connection", "keep-alive")
	if u, err := url.Parse(link); err == nil {
		req.Header.Set("Referer", u.Scheme+"://"+u.Host+"/")
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	}

	clientMu.RLock()
	client := httpClient
	clientMu.RUnlock()

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		defer resp.Body.Close()
		linkURL, err := url.Parse(link)
		if err != nil {
			return nil, errors.New("unknown error occurred")
		}
		redirectURL, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			return nil, errors.New("unknown error occurred")
		}
		newLink := linkURL.ResolveReference(redirectURL)
		return response(newLink.String())
	}

	// Best-effort diagnostics: read a small prefix of the response body.
	// This helps investigate provider blocks (e.g., HTTP 458) without dumping huge responses.
	const maxDiag = 4096
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxDiag))
	_ = resp.Body.Close()
	msg := strings.TrimSpace(string(snippet))
	if len(msg) > 300 {
		msg = msg[:300]
	}
	if msg != "" {
		return nil, errors.New(link + " returned HTTP code " + strconv.Itoa(resp.StatusCode) + ": " + msg)
	}
	return nil, errors.New(link + " returned HTTP code " + strconv.Itoa(resp.StatusCode))
}

func addHeaders(from, to http.Header, contentLength bool) {
	for k, v := range from {
		switch k {
		case "Connection":
			to.Set("Connection", strings.Join(v, "; "))
		case "Content-Type":
			to.Set("Content-Type", strings.Join(v, "; "))
		case "Transfer-Encoding":
			to.Set("Transfer-Encoding", strings.Join(v, "; "))
		case "Cache-Control":
			to.Set("Cache-Control", strings.Join(v, "; "))
		case "Date":
			to.Set("Date", strings.Join(v, "; "))
		case "Content-Length":
			// This is only useful for unaltered media files. It should not be copied for HLS requests because
			// players will not attempt to receive more bytes from HTTP server than are set here, therefore some HLS
			// contents would not load. E.g. CURL would display error "curl: (18) transfer closed with 83 bytes remaining to read"
			// if set for HLS metadata requests.
			if contentLength {
				to.Set("Content-Length", strings.Join(v, "; "))
			}
		}
	}
}

func getLinkType(contentType string) int {
	contentType = strings.ToLower(contentType)
	switch {
	case contentType == "application/vnd.apple.mpegurl" || contentType == "application/x-mpegurl":
		return linkTypeHLS
	case strings.HasPrefix(contentType, "video/") || strings.HasPrefix(contentType, "audio/") || contentType == "application/octet-stream":
		return linkTypeMedia
	default:
		return linkTypeMedia
	}
}
