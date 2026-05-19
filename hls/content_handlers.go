package hls

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
)

func handleContent(cr *ContentRequest) {
	linkType := cr.ChannelRef.LinkType

	if linkType == linkTypeUnknown {
		handleContentUnknown(cr)
		return
	}

	// At this point we will no longer modify channel details, so we get a copy of 'ChannelRef'
	// value and set to 'Channel' so we can avoid synchronization
	cr.Channel = *cr.ChannelRef
	cr.ChannelRef.Mux.Unlock()

	switch linkType {
	case linkTypeHLS:
		handleContentHLS(cr)
	case linkTypeMedia:
		handleContentMedia(cr)
	default:
		http.Error(cr.ResponseWriter, "invalid media type", http.StatusInternalServerError)
	}
}

// escapeM3U8Attr sanitizes attribute values so quotes in channel names cannot break #EXTINF lines.
func escapeM3U8Attr(s string) string {
	s = strings.ReplaceAll(s, `"`, "'")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

// ####################################################

func handleContentUnknown(cr *ContentRequest) {
	resp, err := response(cr.ChannelRef.Link)
	if err != nil {
		cr.ChannelRef.Mux.Unlock()
		http.Error(cr.ResponseWriter, "stream unavailable", http.StatusServiceUnavailable)
		log.Printf("[HLS] probe upstream failed channel=%q err=%v", cr.Title, err)
		return
	}
	defer resp.Body.Close()

	cr.ChannelRef.LinkType = getLinkType(resp.Header.Get("Content-Type"))

	if cr.ChannelRef.LinkType == linkTypeHLS {
		// Initiate new HLS channel
		cr.ChannelRef.HLSLink = resp.Request.URL.String()
		cr.ChannelRef.HLSLinkRoot = deleteAfterLastSlash(cr.ChannelRef.HLSLink)
	}

	handleContent(cr)
}

// ####################################################

func handleContentHLS(cr *ContentRequest) {
	var link string
	if cr.Suffix == "" {
		link = cr.Channel.HLSLink
	} else {
		link = cr.Channel.HLSLinkRoot + cr.Suffix
	}

	resp, err := response(link)
	if err != nil {
		log.Printf("[HLS] segment fetch failed channel=%q suffix=%q err=%v", cr.Title, cr.Suffix, err)
		http.Error(cr.ResponseWriter, "stream unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	handleEstablishedContentHLS(cr, resp, link)
}

func handleEstablishedContentHLS(cr *ContentRequest, resp *http.Response, link string) {
	// Build prefix based on how the client accessed the channel: via /iptv or root
	prefixBase := "/"
	if strings.HasPrefix(cr.Request.URL.Path, "/iptv/") {
		prefixBase = "/iptv/"
	}
	scheme, host := externalBase(cr.Request)
	prefix := scheme + "://" + host + prefixBase + url.PathEscape(cr.Title) + "/"

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	switch {
	case contentType == "application/vnd.apple.mpegurl" || contentType == "application/x-mpegurl": // HLS metadata
		content := rewriteLinks(&resp.Body, prefix, cr.Channel.HLSLinkRoot)
		addHeaders(resp.Header, cr.ResponseWriter.Header(), false)
		cr.ResponseWriter.WriteHeader(http.StatusOK)
		fmt.Fprint(cr.ResponseWriter, content)
	default: // media (or anything else)
		handleEstablishedContentMedia(cr, resp)
	}
}

// ####################################################

func handleContentMedia(cr *ContentRequest) {
	resp, err := response(cr.Channel.Link)
	if err != nil {
		log.Printf("[HLS] media upstream failed channel=%q err=%v", cr.Title, err)
		http.Error(cr.ResponseWriter, "stream unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	handleEstablishedContentMedia(cr, resp)
}

func handleEstablishedContentMedia(cr *ContentRequest, resp *http.Response) {
	addHeaders(resp.Header, cr.ResponseWriter.Header(), true)
	cr.ResponseWriter.WriteHeader(resp.StatusCode)
	io.Copy(cr.ResponseWriter, resp.Body)
}
