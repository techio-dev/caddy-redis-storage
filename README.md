# Caddy Redis Storage

Caddy storage plugin sử dụng Redis làm backend cho Caddy cluster HA. Nhiều Caddy instance chia sẻ TLS certificates qua Redis Standalone.

## Tính năng

- Implements `certmagic.Storage` interface đầy đủ
- MULTI/EXEC transactions cho atomic writes
- Distributed locking với owner token + Lua scripts
- Ancestor directory tracking và best-effort pruning
- Docker image kèm Cloudflare DNS + certmagic-s3

## Cấu hình

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

| Field | Default | Mô tả |
|-------|---------|--------|
| `url` | `redis://localhost:6379/0` | Redis URL. Hỗ trợ `redis://` và `rediss://` (TLS). |
| `prefix` | `caddy` | Prefix cho tất cả Redis keys. |
| `lock_ttl` | `60s` | TTL cho distributed lock. |
| `lock_renew_interval` | `20s` | Interval renew lock lease. Phải < `lock_ttl`. |

## Redis Key Schema

Storage path `/` chuyển thành `:`, type suffix nằm cuối.

| Redis Key | Type | Mục đích |
|-----------|------|----------|
| `caddy:certificates:acme:example.com:example.com.crt:binary` | String | Certificate data |
| `caddy:certificates:acme:example.com:example.com.crt:metadata` | Hash | Metadata (mod, size, terminal) |
| `caddy:certificates:acme:example.com:directory` | Set | Danh sách child names |
| `caddy:certname:lock` | String | Lock owner token (TTL) |

## Docker

### Build

```bash
docker build -t caddy-redis .
```

Image bao gồm:
- `github.com/techio-dev/caddy-redis-storage` — Redis storage backend
- `github.com/caddy-dns/cloudflare` — Cloudflare DNS challenge
- `github.com/ss098/certmagic-s3` — S3 storage cho certmagic

### Run

```bash
docker run -e CONFIG_URL=http://config-server/caddy.json caddy-redis
```

Container khởi động với `default.json` — admin API listen `0.0.0.0:2019`, tự động load config từ `$CONFIG_URL` sau 5s.

### Ví dụ Caddyfile

```
{
    storage redis {
        url redis://redis:6379/0
        prefix caddy
    }
}

example.com {
    respond "Hello from Caddy Redis cluster"
}
```

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

## Build với xcaddy

```bash
xcaddy build \
    --with github.com/techio-dev/caddy-redis-storage \
    --with github.com/caddy-dns/cloudflare \
    --with github.com/ss098/certmagic-s3
```

## Development

### Yêu cầu

- Go 1.26+
- Redis instance cho integration tests

### Tests

```bash
# Unit tests (keys helpers)
go test -v -run "Test" ./...

# Integration tests (cần Redis tại localhost:6379)
docker run -d --name test-redis -p 6379:6379 redis:7-alpine
go test -v -timeout 30s ./...
```

## Deployment

- **Redis Standalone only.** Không support Cluster hay Sentinel.
- Nhiều Caddy nodes kết nối đến cùng 1 Redis instance.
- Chỉ 1 node thực hiện certificate acquisition tại một thời điểm (distributed lock).

## License

Apache License 2.0
