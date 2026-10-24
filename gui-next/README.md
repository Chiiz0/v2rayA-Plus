# v2rayA-Plus modern interface

Vue 3, Vuetify, Pinia and Vite interface adapted from upstream v2rayA commit
`1ee48148dd5422f5de8ace837d17883066ac7e67`. This directory is the default UI;
`../gui` remains available at `/legacy/`. Both use the same authenticated API.

Use Node.js 24 and Yarn 1.22.22:

```sh
corepack enable
corepack prepare yarn@1.22.22 --activate
yarn install --frozen-lockfile --ignore-engines
yarn typecheck
yarn i18n-check
yarn lint
yarn test
OUTPUT_DIR=../service/server/router/web yarn build
# Build classic assets after the modern build, which clears its output directory.
(cd ../gui && yarn install --frozen-lockfile --ignore-engines && OUTPUT_DIR=../service/server/router/web/legacy yarn build)
```

For development, run `yarn serve` and set the service address in the login dialog.
A production build supports reverse-proxy path prefixes. Administrative pages
are served from the deployed binary without installing an offline service worker.

Fork-specific compatibility includes independent Docker SOCKS proxies, front
proxy chains, V2Fly/Xray selection, SSR, VLESS encryption, and lossless XHTTP extra
parameters. Settings patch only changed supported fields. DNS uses the local
internal/external list API. Upstream custom inbounds, URL routing imports,
TUN scripts and explicit pinned outbound selection are not exposed because the
fork backend does not implement those contracts.

See [the migration notes](../docs/upstream-migration.md) and
[release rules](../docs/releases.md).
