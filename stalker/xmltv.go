package stalker

import (
	"encoding/xml"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"
)

// XMLTVOptions controls generated guide size (large portals need limits).
type XMLTVOptions struct {
	IncludePrograms bool
	ProgramLimit    int // max channels to fetch EPG for
	EPGSize         int // programs per channel
}

// BuildXMLTV generates an XMLTV document for the given channel map.
// Programme times use loc when non-nil (portal timezone).
func BuildXMLTV(channels map[string]*Channel, opts XMLTVOptions, loc *time.Location) ([]byte, error) {
	if loc == nil {
		loc = time.UTC
	}
	if opts.EPGSize <= 0 {
		opts.EPGSize = 4
	}
	if opts.ProgramLimit <= 0 {
		opts.ProgramLimit = 150
	}
	type xmltv struct {
		XMLName    xml.Name         `xml:"tv"`
		Generator  string           `xml:"generator,attr"`
		Channels   []xmlChannel     `xml:"channel"`
		Programmes []xmlProgramme   `xml:"programme"`
	}
	doc := xmltv{Generator: "stalkerhek"}
	keys := make([]string, 0, len(channels))
	for k := range channels {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return CompareNatural(keys[i], keys[j]) < 0 })
	progFetched := 0
	for _, title := range keys {
		ch := channels[title]
		if ch == nil {
			continue
		}
		id := ch.EPGChannelID()
		if id == "" {
			continue
		}
		display := CleanTitleForM3U8(title)
		if display == "" {
			display = title
		}
		xc := xmlChannel{
			ID:          id,
			DisplayName: display,
		}
		if logo := ch.Logo(); logo != "" {
			xc.Icon = &xmlIcon{Src: logo}
		}
		doc.Channels = append(doc.Channels, xc)
		if !opts.IncludePrograms || progFetched >= opts.ProgramLimit {
			continue
		}
		programs, err := ch.GetShortEPG(opts.EPGSize)
		if err != nil || len(programs) == 0 {
			programs, _ = ch.GetEPGInfo()
		}
		for _, p := range programs {
			if p.Start.IsZero() {
				continue
			}
			stop := p.Stop
			if stop.IsZero() {
				stop = p.Start.Add(30 * time.Minute)
			}
			start := p.Start.In(loc)
			stopT := stop.In(loc)
			doc.Programmes = append(doc.Programmes, xmlProgramme{
				Channel: id,
				Start:   start.Format("20060102150405 -0700"),
				Stop:    stopT.Format("20060102150405 -0700"),
				Title:   p.Title,
				Desc:    p.Description,
				Category:p.Category,
			})
		}
		progFetched++
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), out...), nil
}

type xmlChannel struct {
	ID          string   `xml:"id,attr"`
	DisplayName string   `xml:"display-name"`
	Icon        *xmlIcon `xml:"icon,omitempty"`
}

type xmlIcon struct {
	Src string `xml:"src,attr"`
}

type xmlProgramme struct {
	Channel  string `xml:"channel,attr"`
	Start    string `xml:"start,attr"`
	Stop     string `xml:"stop,attr"`
	Title    string `xml:"title"`
	Desc     string `xml:"desc,omitempty"`
	Category string `xml:"category,omitempty"`
}

// SanitizeXMLTV escapes text for XML character data.
func SanitizeXMLTV(s string) string {
	return html.EscapeString(strings.TrimSpace(s))
}

// XMLTVContentType is the standard MIME type for XMLTV.
const XMLTVContentType = "application/xml; charset=utf-8"

// FormatXMLTVURL builds the default XMLTV URL for a profile on the WebUI host.
func FormatXMLTVURL(scheme, host string, profileID int) string {
	return fmt.Sprintf("%s://%s/epg/%d/xmltv.xml?programs=1&limit=500", scheme, host, profileID)
}
