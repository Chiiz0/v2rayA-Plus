# RoutingA and rules

Rule ports support bypassing mainland China, GFWList and RoutingA. Transparent proxy follows the same policy when configured to follow rule ports.

Edit RoutingA as a form or text, import/export local text files, or insert a template. The backend validates saved text. Errors keep the draft intact, and existing rules are preserved verbatim.

```text
default: proxy
domain(geosite:cn)->direct
ip(geoip:private,geoip:cn)->direct
```

Rules depend on categories available in your geoip.dat and geosite.dat. Create an outbound group and connect servers before referencing it. This version does not provide remote RoutingA URL imports or scheduled synchronization.

GFWList can be updated manually or automatically. Check the local version after an update. Updating it preserves unsaved settings drafts.
