package coreObj

import "strings"

// Include Vision's UDP/443 variant as well as the base flow.
func IsVisionFlow(flow string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(flow)), "xtls-rprx-vision")
}

func (o OutboundObject) UsesVision() bool {
	if !strings.EqualFold(o.Protocol, "vless") {
		return false
	}
	servers, _ := o.Settings.Vnext.([]Vnext)
	for _, server := range servers {
		for _, user := range server.Users {
			if IsVisionFlow(user.Flow) {
				return true
			}
		}
	}
	return false
}
