package webui

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type epgCacheEntry struct {
	body        []byte
	contentType string
	fetched     time.Time
}

var (
	epgCacheMu sync.RWMutex
	epgCache   = map[int]epgCacheEntry{}
)

const epgCustomCacheTTL = time.Hour

// epgHTTPClient does not auto-decompress responses so .xml.gz URLs stay predictable.
var epgHTTPClient = &http.Client{
	Timeout: 90 * time.Second,
	Transport: &http.Transport{
		DisableCompression: true,
	},
}

// FetchCustomEPG downloads an external XMLTV guide (supports .xml and .xml.gz).
func FetchCustomEPG(profileID int, rawURL string) ([]byte, string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, "", fmt.Errorf("empty epg url")
	}
	low := strings.ToLower(rawURL)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		return nil, "", fmt.Errorf("epg url must be http or https")
	}

	epgCacheMu.RLock()
	if hit, ok := epgCache[profileID]; ok && time.Since(hit.fetched) < epgCustomCacheTTL {
		epgCacheMu.RUnlock()
		return hit.body, hit.contentType, nil
	}
	epgCacheMu.RUnlock()

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "stalkerhek/1.0")
	req.Header.Set("Accept", "application/xml, text/xml, */*")

	resp, err := epgHTTPClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("epg source returned HTTP %d", resp.StatusCode)
	}

	const maxEPG = 64 << 20 // 64 MiB
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxEPG))
	if err != nil {
		return nil, "", err
	}
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("epg source returned empty body")
	}

	body, err := decodeEPGPayload(raw, rawURL, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, "", err
	}
	if len(body) == 0 {
		return nil, "", fmt.Errorf("epg source returned empty guide after decode")
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" || strings.Contains(strings.ToLower(ct), "gzip") {
		ct = "application/xml; charset=utf-8"
	}

	epgCacheMu.Lock()
	epgCache[profileID] = epgCacheEntry{body: body, contentType: ct, fetched: time.Now()}
	epgCacheMu.Unlock()
	return body, ct, nil
}

func decodeEPGPayload(raw []byte, rawURL, contentEncoding string) ([]byte, error) {
	needsGunzip := strings.HasSuffix(strings.ToLower(rawURL), ".gz") ||
		strings.EqualFold(strings.TrimSpace(contentEncoding), "gzip") ||
		isGzipMagic(raw)
	if !needsGunzip {
		return raw, nil
	}
	if !isGzipMagic(raw) {
		// Some hosts serve pre-expanded XML at a .xml.gz URL.
		if strings.HasPrefix(strings.TrimSpace(string(raw)), "<?xml") ||
			strings.Contains(strings.ToLower(string(raw[:min(64, len(raw))])), "<tv") {
			return raw, nil
		}
		return nil, fmt.Errorf("epg gzip decode: not a gzip stream")
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("epg gzip decode: %w", err)
	}
	defer gz.Close()
	out, err := io.ReadAll(io.LimitReader(gz, 64<<20))
	if err != nil {
		return nil, err
	}
	return out, nil
}

func isGzipMagic(b []byte) bool {
	return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func InvalidateEPGCache(profileID int) {
	epgCacheMu.Lock()
	delete(epgCache, profileID)
	epgCacheMu.Unlock()
}
