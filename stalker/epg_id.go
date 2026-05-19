package stalker

import (
	"strings"
)

// EPGChannelID returns the portal channel identifier used for Stalker EPG APIs and XMLTV.
func (c *Channel) EPGChannelID() string {
	if c == nil {
		return ""
	}
	for _, v := range []string{c.CMD_CH_ID, c.CMD_ID} {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}
