# Build stage: xcaddy builds Caddy with modules
FROM caddy:2.11.2-builder-alpine AS builder

ARG MODULE_VERSION

RUN if [ -n "$MODULE_VERSION" ]; then \
      VERSION_TAG="@${MODULE_VERSION}"; \
    else \
      VERSION_TAG=""; \
    fi && \
    xcaddy build \
    --with "github.com/techio-dev/caddy-redis-storage${VERSION_TAG}" \
    --with github.com/caddy-dns/cloudflare \
    --with github.com/ss098/certmagic-s3 \
    --output /usr/bin/caddy

# Runtime stage
FROM caddy:2.11.2-alpine

COPY --from=builder /usr/bin/caddy /usr/bin/caddy
COPY default.json /config/default.json

CMD ["caddy", "run", "--config=/config/default.json"]
