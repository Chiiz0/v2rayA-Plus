# Transparent proxy

Transparent proxy can be off, global, bypass mainland China, use GFWList, or follow the rule ports. Changes take effect when Settings are saved and applied.

Linux supports redirect, tproxy, gVisor TUN and system TUN. Availability depends on privileges, network devices and container configuration. System proxy changes supported desktop proxy settings and does not capture all traffic.

IP forwarding permits forwarding traffic from other devices. Port sharing exposes proxy listeners to other devices according to your deployment. Tproxy white IP groups and custom CIDRs bypass proxying.

DNS anti-pollution offers off, anti-hijacking, forwarding, DoH and custom lists. Enter one `server->outbound` per line in DNS settings, for example `223.5.5.5->direct` or `https://dns.google/dns-query->proxy`. An empty external list uses the internal servers for all queries. FakeDNS requires DNS anti-pollution; supervisor monitors DNS pollution.
