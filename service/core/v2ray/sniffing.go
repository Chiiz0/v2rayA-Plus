package v2ray

import (
	"strings"

	"github.com/v2rayA/v2rayA/core/coreObj"
	"github.com/v2rayA/v2rayA/db/configure"
)

func (t *Template) setInboundSniffing(setting *configure.Setting, excluded string) {
	var domainsExcluded []string
	for _, domain := range strings.Split(excluded, "\n") {
		if domain = strings.TrimSpace(domain); domain != "" {
			domainsExcluded = append(domainsExcluded, domain)
		}
	}
	for i := range t.Inbounds {
		in := &t.Inbounds[i]
		// DNS forwarding is not application traffic. In particular, do not
		// replace its target with an HTTP/TLS/QUIC sniffing result.
		if in.Tag == "dns-in" || in.Tag == "dns-in-local" || in.Tag == "api-in" {
			continue
		}
		in.Sniffing = coreObj.Sniffing{}
		if setting.InboundSniffing == configure.InboundSniffingDisable || setting.InboundSniffing == "" {
			continue
		}
		in.Sniffing.Enabled = true
		in.Sniffing.DestOverride = []string{"http", "tls"}
		if setting.InboundSniffing != configure.InboundSniffingHttpTLS {
			in.Sniffing.DestOverride = append(in.Sniffing.DestOverride, "quic")
		}
		in.Sniffing.DomainsExcluded = domainsExcluded
		// Use the supplied settings, including an unsaved settings update.
		in.Sniffing.RouteOnly = setting.RouteOnly
	}
	t.protectVisionSniffing()
}

func (t *Template) protectVisionSniffing() {
	vision := false
	for _, out := range t.Outbounds {
		if out.UsesVision() {
			vision = true
			break
		}
	}
	if !vision {
		return
	}
	for i := range t.Inbounds {
		in := &t.Inbounds[i]
		tag := strings.TrimSuffix(strings.TrimSuffix(in.Tag, "_ipv6"), "_ipv4")
		transparent := tag == "transparent" ||
			(in.Settings != nil && in.Settings.FollowRedirect)
		if transparent && in.Sniffing.Enabled {
			// Keep the original destination; sniffed domains only select a route.
			// This also covers TUN's SOCKS inbound and does not enable sniffing
			// when the user disabled it. FakeDNS keeps its core-defined override.
			in.Sniffing.RouteOnly = true
		}
	}
}
