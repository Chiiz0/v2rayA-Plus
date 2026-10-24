#!/usr/bin/env bash
# Verify DNS interception and recovery across fresh containers without touching
# the host resolver, its network namespace, or its firewall.
set -euo pipefail
repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="$(mktemp -d -t v2raya-dns-test.XXXXXXXX)"
trap 'rm -rf -- "$test_dir"' EXIT
mkdir "$test_dir/backup"
printf '# test resolver\nsearch local.example\noptions timeout:2\nnameserver 192.0.2.53\n' > "$test_dir/original"
cp "$test_dir/original" "$test_dir/resolv.conf"
chmod 0640 "$test_dir/resolv.conf"
(
  cd "$repo_dir/service"
  CGO_ENABLED=0 go test -c -o "$test_dir/resolv.test" ./core/v2ray
)
for stage in hijack recover; do
  docker run --rm --network none \
    --mount "type=bind,src=$test_dir/resolv.test,dst=/resolv.test,readonly" \
    --mount "type=bind,src=$test_dir/original,dst=/dns-bind-test-original,readonly" \
    --mount "type=bind,src=$test_dir/resolv.conf,dst=/dns-bind-test-resolv.conf" \
    --mount "type=bind,src=$test_dir/backup,dst=/dns-bind-test-backup" \
    -e "V2RAYA_TEST_RESOLV_STAGE=$stage" \
    alpine:3.22 /resolv.test -test.v -test.run '^TestResolvDockerBindMount$'
done
cmp "$test_dir/original" "$test_dir/resolv.conf"
