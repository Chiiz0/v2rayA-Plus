# Startup parameters and database

Startup flags are set in the service command, environment or container configuration. Run `v2raya --help` using the installed binary for its exact options. Restart or recreate the service after changing its environment.

Common settings include V2RAYA_ADDRESS (management listener), V2RAYA_CONFIG (configuration directory) and V2RAYA_V2RAY_BIN (core path).

This project retains bbolt support and provides a validated migration path to SQLite. A UI upgrade does not select a new storage backend. Back up the complete configuration directory before migration, follow the repository migration instructions, and retain recoverable original data.

Database migration, restoration and service lifecycle operations belong in your deployment environment. This interface does not claim to provide online database administration endpoints.
