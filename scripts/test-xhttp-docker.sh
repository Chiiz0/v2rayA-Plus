#!/usr/bin/env bash
# The integration test creates and cleans up its own internal Docker network,
# containers and temporary configs; it never publishes ports or edits host DNS.
set -euo pipefail
repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir/service"
V2RAYA_TEST_XHTTP_DOCKER=1 go test ./server/service -run '^TestXHTTPDockerIntegration$' -count=1 -v
