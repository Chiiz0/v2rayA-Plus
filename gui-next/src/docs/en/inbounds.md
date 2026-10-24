# Addresses and ports

Open Settings → Address and ports to configure SOCKS5, HTTP, rule-based SOCKS5/HTTP, VMess and the core API. Valid ports are 0–65535; 0 disables a listener. The service reports port conflicts or permission errors.

An enabled VMess inbound provides a share link. Select HandlerService, LoggerService or StatsService for the core API as needed.

The backend address tells this browser where the management service is. An empty value uses the current page origin. It does not change the server listener; use V2RAYA_ADDRESS at startup for that. Docker deployments also require suitable networking or port mappings.

This version does not expose the newer upstream arbitrary JSON inbound management API.
