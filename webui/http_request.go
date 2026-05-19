package webui

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// requestSchemeHost returns the external scheme and host for URLs shown to IPTV clients.
// Honors X-Forwarded-Proto / X-Forwarded-Host when behind a reverse proxy.
func requestSchemeHost(r *http.Request) (scheme, host string) {
	scheme = "http"
	host = "localhost:4400"
	if r == nil {
		return scheme, host
	}
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := firstHeaderValue(r.Header.Get("X-Forwarded-Proto")); xf != "" {
		scheme = strings.ToLower(xf)
	}
	if h := strings.TrimSpace(r.Host); h != "" {
		host = h
	}
	if xf := firstHeaderValue(r.Header.Get("X-Forwarded-Host")); xf != "" {
		host = xf
	}
	return scheme, host
}

func firstHeaderValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if i := strings.Index(v, ","); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// writeJSON encodes v as JSON; logs encode failures without panicking.
func writeJSON(w http.ResponseWriter, v any) {
	if w == nil {
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[webui] JSON response encode failed: %v", err)
	}
}

func extOrDefault(ext, def string) string {
	ext = strings.TrimSpace(strings.TrimPrefix(ext, "."))
	if ext == "" {
		return def
	}
	return ext
}
