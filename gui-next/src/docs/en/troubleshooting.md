# Troubleshooting

Read the full backend error after a failed save. Settings, DNS and RoutingA retain rejected drafts for correction. Failed initial loads cannot replace existing data with empty defaults.

For connection failures, check the server parameters, core version and logs. Successful import does not prove connectivity. Transparent proxy also requires system permissions and network support. Verify ordinary SOCKS5/HTTP proxying before diagnosing transparent routes and DNS.

For DNS failures, check anti-pollution mode, internal/external server lists and system DNS. Check for occupied ports and avoid recursive references to the local resolver itself.

Stop the writing service before restoring a backup, and keep the current directory for rollback. Include the version, reproduction steps and redacted logs when reporting issues. Do not publish subscription URLs, passwords or keys.

[Report a v2rayA-Plus issue](https://github.com/Chiiz0/V2rayA-Plus/issues)
