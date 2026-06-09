FROM alpine AS version
WORKDIR /build
COPY . /build
RUN apk add --no-cache git 2>/dev/null || true
RUN if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then \
      git describe --tags --abbrev=0 > ./version || echo "v2rayA-Plus" > ./version; \
    else \
      echo "v2rayA-Plus" > ./version; \
    fi
# Strip leading 'v' for semver tags (e.g. v1.2.3 -> 1.2.3); keep custom strings as-is
RUN VERSION=$(cat ./version); \
    if echo "$VERSION" | grep -qE '^v[0-9]' && ! echo "$VERSION" | grep -qE '^v2rayA'; then \
      echo "${VERSION#v}" > ./version; \
    fi


FROM node:20-alpine AS builder-web
ADD gui /build/gui
WORKDIR /build/gui
RUN echo "network-timeout 600000" >> .yarnrc
#RUN yarn config set registry https://registry.npm.taobao.org
#RUN yarn config set sass_binary_site https://cdn.npm.taobao.org/dist/node-sass -g
RUN yarn cache clean && yarn install --ignore-engines && yarn build

FROM golang:alpine AS builder
ADD service /build/service
WORKDIR /build/service
COPY --from=version /build/version ./
COPY --from=builder-web /build/web server/router/web
RUN CGO_ENABLED=0 go build -ldflags="-X github.com/v2rayA/v2rayA/conf.Version=$(cat ./version) -s -w" -o v2raya-plus .

FROM v2fly/v2fly-core
COPY --from=builder /build/service/v2raya-plus /usr/bin/
RUN wget -O /usr/local/share/v2ray/LoyalsoldierSite.dat https://raw.githubusercontent.com/mzz2017/dist-v2ray-rules-dat/master/geosite.dat
RUN apk add --no-cache iptables ip6tables tzdata docker-cli
LABEL org.opencontainers.image.source=https://github.com/v2rayA/v2rayA
EXPOSE 2017
ENV V2RAYA_CONFIG /etc/v2raya-plus
VOLUME /etc/v2raya-plus
ENTRYPOINT ["v2raya-plus"]
