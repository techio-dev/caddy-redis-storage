# Caddy Redis Storage

A [Caddy](https://caddyserver.com/) storage plugin that uses **Redis Standalone** as the backend for sharing TLS certificates across multiple Caddy instances.

## How It Works

The plugin implements the `certmagic.Storage` interface:

- **Storage operations** — `Store`, `Load`, `Delete`, `Exists`, `List`, `Stat` map certificate data to Redis keys using a suffix-type + colon-path schema. Multi-key writes use `MULTI/EXEC` transactions for atomicity.
- **Distributed locking** — `Lock`, `Unlock`, `TryLock`, `RenewLockLease` use per-lock owner tokens with Lua scripts for atomic compare-and-swap operations. Locks auto-expire via TTL and are renewed in the background.
- **Directory tracking** — Redis Sets track child names at each path level, enabling recursive `List` and `Delete`. Empty ancestor directories are pruned on a best-effort basis after deletes.

**One constraint:** Redis Standalone only. No Cluster or Sentinel support.

## Configuration

Add a `storage` block to your Caddy JSON config:

```json
{
  "storage": {
    "module": "redis",
    "url": "redis://localhost:6379/0",
    "prefix": "caddy",
    "lock_ttl": "60s",
    "lock_renew_interval": "20s"
  }
}
```

### Fields

| Field | Default | Description |
|-------|---------|-------------|
| `url` | `redis://localhost:6379/0` | Redis URL. Supports `redis://` and `rediss://` (TLS). Supports Caddy placeholders like `{env.REDIS_URL}`. |
| `prefix` | `caddy` | Prefix for all Redis keys. |
| `lock_ttl` | `60s` | TTL for distributed locks. |
| `lock_renew_interval` | `20s` | Interval for lock lease renewal. Must be less than `lock_ttl`. |

### Environment Variables

Caddy placeholders (`{env.*}`) are resolved in all string fields. This is useful for injecting secrets:

```json
{
  "storage": {
    "module": "redis",
    "url": "{env.REDIS_URL}"
  }
}
```

### Redis Key Schema

Storage path separators (`/`) are converted to colons. A type suffix is appended to each key.

| Redis Key | Type | Purpose |
|-----------|------|---------|
| `caddy:certificates:acme:example.com:example.com.crt:binary` | String | Certificate data |
| `caddy:certificates:acme:example.com:example.com.crt:metadata` | Hash | Metadata (modified, size, terminal) |
| `caddy:certificates:acme:example.com:directory` | Set | Child name list |
| `caddy:certname:lock` | String | Lock owner token (with TTL) |

## Building

### Docker (Recommended)

```bash
docker build -t caddy-redis .
```

The image includes:
- `github.com/techio-dev/caddy-redis-storage` — this plugin
- `github.com/caddy-dns/cloudflare` — Cloudflare DNS challenge
- `github.com/ss098/certmagic-s3` — S3 storage for certmagic

#### Build a Specific Version

Pass the `MODULE_VERSION` build arg to pin the plugin version:

```bash
docker build --build-arg MODULE_VERSION=v1.0.0 -t caddy-redis:v1.0.0 .
```

Without this arg, the build uses the latest commit on the default branch.

### xcaddy

Build Caddy locally with the plugin:

```bash
xcaddy build \
    --with github.com/techio-dev/caddy-redis-storage@v1.0.0 \
    --with github.com/caddy-dns/cloudflare \
    --with github.com/ss098/certmagic-s3
```

Drop the `@v1.0.0` suffix to build from the latest commit.

## Running

### Docker

```bash
docker run -e CONFIG_URL=http://config-server/caddy.json caddy-redis
```

The container starts with a default config that:
1. Exposes the admin API on `0.0.0.0:2019`
2. Loads the full config from `$CONFIG_URL` after a 5-second delay

### Docker Compose

```yaml
services:
  caddy:
    image: caddy-redis
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp"
      - "2019:2019"
    environment:
      - CONFIG_URL=http://config-server/caddy.json
    depends_on:
      - redis

  redis:
    image: redis:7-alpine
    volumes:
      - redis-data:/data
    command: redis-server --appendonly yes

volumes:
  redis-data:
```

### Full Config Example

A typical Caddy JSON config using Redis storage with environment variables:

```json
{
  "storage": {
    "module": "redis",
    "url": "{env.REDIS_URL}",
    "prefix": "caddy"
  },
  "apps": {
    "http": {
      "servers": {
        "example": {
          "listen": [":443"],
          "routes": [
            {
              "match": [{"host": ["example.com"]}],
              "handle": [{"handler": "static_response", "body": "Hello from Caddy Redis cluster"}]
            }
          ],
          "automatic_https": {
            "disable": false
          }
        }
      }
    },
    "tls": {
      "automation": {
        "policies": [
          {
            "subjects": ["example.com"],
            "issuers": [
              {
                "module": "acme",
                "challenges": {
                  "dns": {
                    "provider": {
                      "name": "cloudflare",
                      "api_token": "{env.CF_API_TOKEN}"
                    }
                  }
                }
              }
            ]
          }
        ]
      }
    }
  }
}
```

## Development

### Requirements

- Go 1.24+
- Redis instance for integration tests

### Tests

```bash
# Unit tests
go test -v -run "TestKeys" ./...

# Integration tests (requires Redis on localhost:6379)
docker run -d --name test-redis -p 6379:6379 redis:7-alpine
go test -v -timeout 30s ./...
```

## Deployment Notes

- Multiple Caddy nodes connect to the same Redis instance.
- Only one node performs certificate acquisition at a time (distributed lock).
- All nodes read certificates from Redis — no local storage needed.

## License

Apache License 2.0
