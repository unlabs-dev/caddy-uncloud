ARG CADDY_VERSION=2.11.7

FROM caddy:${CADDY_VERSION}-builder AS builder
ARG STORAGE_VERSION
# Temporary CertMagic fork fixes distributed ACME challenge lookup for staging retries.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    xcaddy build \
        --replace github.com/caddyserver/certmagic=github.com/psviderski/certmagic@8e07731d130b6346f6c3441512e55ef6c926295d \
        --with github.com/unlabs-dev/caddy-uncloud/storage@${STORAGE_VERSION} \
        --with github.com/mholt/caddy-l4@v0.1.2 \
        --with github.com/caddy-dns/cloudflare@v0.2.4 \
        --with github.com/WeidiDeng/caddy-cloudflare-ip@f53b62aa13cb7ad79c8b47aacc3f2f03989b67e5

FROM caddy:${CADDY_VERSION}
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
