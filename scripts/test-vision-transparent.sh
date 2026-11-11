#!/usr/bin/env bash
set -euo pipefail

# The image must contain xray, v2ray and nft. No host network or firewall access.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT
test_image="${V2RAYA_VISION_TEST_IMAGE:-v2raya-plus:latest}"
(
  cd "$repo_root/service"
  CGO_ENABLED=0 go test -c -o "$test_dir/vision.test" ./core/v2ray
)
docker run --rm --network none --cap-add NET_ADMIN \
  -e V2RAYA_TEST_VISION_NETNS=1 -e V2RAYA_CONFIG=/tmp/v2raya-test \
  -v "$test_dir/vision.test:/tmp/vision.test:ro" \
  --entrypoint /tmp/vision.test "$test_image" \
  -test.run '^TestVisionRedirectIntegration$' -test.v -test.timeout 60s
