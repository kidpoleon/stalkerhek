package hls

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/kidpoleon/stalkerhek/stalker"
)

var (
	hlsLinkTTL   atomic.Int64 // nanoseconds
	mediaLinkTTL atomic.Int64
)

func init() {
	SetChannelLinkTTL(180*time.Second, 45*time.Second)
}

// SetChannelLinkTTL controls how long a resolved upstream link is reused before
// re-requesting create_link from the portal. Short TTLs cause visible hiccups.
func SetChannelLinkTTL(hlsTTL, mediaTTL time.Duration) {
	if hlsTTL < 30*time.Second {
		hlsTTL = 30 * time.Second
	}
	if mediaTTL < 5*time.Second {
		mediaTTL = 5 * time.Second
	}
	hlsLinkTTL.Store(int64(hlsTTL))
	mediaLinkTTL.Store(int64(mediaTTL))
}
const (
	linkTypeUnknown = 0 // default
	linkTypeHLS     = 1
	linkTypeMedia   = 2
)

// Logo stores TV channel logo details.
type Logo struct {
	Mux              *sync.Mutex
	Link             string // Link to channel's URL
	Cache            []byte // Actual logo
	CacheContentType string // Logo type
}

// Channel stores TV channel details.
type Channel struct {
	StalkerChannel *stalker.Channel // Reference to Stalker channel

	Mux *sync.Mutex // Mux for channel.

	Link     string // Original link, retrieved from Stalkerhek middleware
	LinkType int    // Original link's type

	HLSLink     string // Updated HLS TV channel's link
	HLSLinkRoot string // Used for HLS relative paths

	lastAccess time.Time // Last access time of this channel, so we know when to request new channel from Stalker middleware

	Logo *Logo // Reference to channel's logo

	Genre    string // TV channel genre, title-cased. Used by filter UI display.
	RawGenre string // Portal genre string, unmodified casing. Used for M3U8 output.
}

func (c *Channel) validate() error {
	if !c.isValid() {
		newLink, err := c.StalkerChannel.NewLink(false)
		if err != nil {
			return err
		}

		c.Link = newLink
		c.LinkType = 0
		c.HLSLink = ""
		c.HLSLinkRoot = ""
	}

	c.lastAccess = time.Now()
	return nil
}

func (c *Channel) isValid() bool {
	if c.lastAccess.IsZero() {
		return false
	}
	ttl := time.Duration(mediaLinkTTL.Load())
	if c.LinkType == linkTypeHLS {
		ttl = time.Duration(hlsLinkTTL.Load())
	}
	return time.Since(c.lastAccess) <= ttl
}