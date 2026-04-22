# Caddy Redis Storage Plugin — Design Spec

## Overview

Caddy plugin implement `certmagic.Storage` interface, sử dụng Redis làm storage backend cho Caddy cluster HA. Nhiều Caddy instance chia sẻ TLS certificates qua Redis.

## Requirements

- Implement `certmagic.Storage` interface (Store/Load/Delete/Exists/List/Stat + Locker)
- Implement `caddy.StorageConverter` interface
- Distributed locking cho certificate acquisition coordination
- Redis Standalone only
- Caddy module registration chuẩn (`caddy.storage.redis`)

## Architecture

### Project structure

```
caddy-redis-storage/
├── go.mod
├── go.sum
├── caddy.go         # Module registration, CaddyModule(), Provision(), StorageConverter
├── storage.go       # certmagic.Storage: Store/Load/Delete/Exists/List/Stat
├── lock.go          # Locker + TryLocker + LockLeaseRenewer
├── keys.go          # Redis key schema helpers
└── caddy_test.go    # Integration tests
```

### Module registration

```go
func init() {
    caddy.RegisterModule(RedisStorage{})
}

func (RedisStorage) CaddyModule() caddy.ModuleInfo {
    return caddy.ModuleInfo{
        ID:  "caddy.storage.redis",
        New: func() caddy.Module { return new(RedisStorage) },
    }
}
```

Implements `caddy.StorageConverter`:
```go
func (r *RedisStorage) CertMagicStorage() (certmagic.Storage, error) {
    return r, nil
}
```

### Configuration

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

URL format: `redis://[user:password@]host:port/db` hoặc `rediss://` cho TLS.
Parsed bằng `redis.ParseURL()` từ `redis/go-redis`.

### Redis key schema

Storage path `/` được chuyển thành `:` trong Redis key. Type suffix nằm cuối key.

| Key pattern | Type | Purpose |
|-------------|------|---------|
| `{prefix}:{colon_path}:binary` | String | Binary data |
| `{prefix}:{colon_path}:metadata` | Hash | Metadata: mod (unix ms), size, terminal (1/0) |
| `{prefix}:{colon_path}:directory` | Set | Child names under directory |
| `{prefix}:{colon_path}:lock` | String | Lock owner token (TTL) |

`{colon_path}` = storage key với `/` thay bằng `:`. Ví dụ: `certificates/acme/example.com` → `certificates:acme:example.com`.
Root directory (path rỗng) dùng sentinel: `__root__`.

Default prefix: `caddy`.

Example with key `certificates/acme/example.com/example.com.crt`:
- `caddy:certificates:acme:example.com:example.com.crt:binary` → cert binary
- `caddy:certificates:acme:example.com:example.com.crt:metadata` → `{mod: "1710000000000", size: "3842", terminal: "1"}`
- `caddy:certificates:acme:example.com:directory` → set: `{"example.com.crt", "example.com.key", "example.com.json"}`

**Lợi ích:**
- Group by path: tất cả Redis keys cho cùng 1 storage key nằm liền nhau khi scan
- Suffix xác định type ngay: dễ filter theo `:binary`, `:metadata`, `:directory`, `:lock`
- Không có `/` trong Redis key — clean namespace
- Không viết tắt — dễ đọc, dễ debug khi scan Redis

### Collision safety

CertMagic `StorageKeys.Safe()` thay `:` thành `-` trong mọi key component. Do đó CertMagic-generated keys không bao giờ chứa `:`, loại bỏ hoàn toàn collision risk khi dùng colon-path encoding.

## Distributed Locking

### Lock acquisition

```
Lock(ctx, name):
  1. Generate ownerToken = UUIDv4
  2. SET {prefix}:{name_colon}:lock {ownerToken} NX PX {lock_ttl_ms}
  3. Nếu OK → acquired. Lưu local: locks[name] = ownerToken
  4. Nếu không OK → retry loop:
     - Exponential backoff: 500ms → 1s → 2s (cap 2s)
     - Random jitter ±200ms
     - Check ctx.Done() mỗi iteration
```

### Lock release (Lua — atomic compare-and-delete)

```lua
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end
```

### Lock renewal (Lua — atomic compare-and-pexpire)

```lua
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("PEXPIRE", KEYS[1], ARGV[2])
else
    return 0
end
```

### TryLock

```
TryLock(ctx, name):
  1. Generate ownerToken = UUIDv4
  2. SET NX PX → return (true, nil) hoặc (false, nil)
  3. Không retry
```

### Lock TTL defaults

- `lock_ttl`: 60s
- `lock_renew_interval`: 20s

### Local state

```go
type RedisStorage struct {
    mu    sync.Mutex
    locks map[string]string  // lockName → ownerToken
}
```

**Lifecycle rules:**
- `Unlock(name)`: nếu name không có trong local map → return error "lock not held by this instance".
- `RenewLockLease(lockKey)`: nếu lockKey không có trong local map → return error.
- `Lock()` + `TryLock()` dùng cùng local map.
- Reentrant lock không supported — gọi `Lock()` 2 lần cùng name sẽ block ở lần thứ 2.
- Config validation: `lock_renew_interval` phải < `lock_ttl`. Reject zero/negative values.

**Stale lease caveat:** do không có fencing tokens, nếu GC pause/network partition > TTL, old owner có thể tiếp tục chạy sau khi owner mới acquire lock. CertMagic accepts rare mis-synchronizations. Acceptable cho use case này.

## Invariants

- File key exists iff `:binary` exists. `:metadata` luôn đi kèm sau `Store()`.
- Directory key exists iff `:directory` set exists.
- `Store()` và `Delete()` dùng MULTI/EXEC transactions — binary + metadata + directory index commit atomically.
- A key phải là file hoặc directory, không được cả hai. Nếu cả hai tồn tại do corruption → xóa cả hai khi `Delete()`, reads ưu tiên `:binary`.

## Key Normalization

- Plugin assumes caller passes valid `certmagic` keys (no leading/trailing `/`, no empty components, no `.`/`..`).
- Internal helpers normalize với `path.Clean()` trước khi transform.
- CertMagic `StorageKeys.Safe()` đã sanitize key components (thay `:` thành `-`), nên collision risk gần bằng 0 cho use case thực tế.

## Storage Methods

### Store(ctx, key, value)

MULTI/EXEC transaction (atomic):
1. `SET {prefix}:{key_colon}:binary {value}`
2. `HSET {prefix}:{key_colon}:metadata mod {now_unix_ms} size {len(value)} terminal 1`
3. `SADD {prefix}:{parent_colon}:directory {basename}`
4. Ancestor directory insertion: SADD basename vào parent directory set cho mỗi ancestor level.

Parent path extraction: `path.Dir(key)`. Basename: `path.Base(key)`.
`{key_colon}` = storage key với `/` → `:`. `{parent_colon}` = parent path với `/` → `:`.
Ancestor directories: khi `Store()`, đảm bảo mỗi ancestor path có entry trong directory set của parent.
Ví dụ với key `a/b/c/file.txt`: SADD `caddy:a:directory` "b", SADD `caddy:a:b:directory` "c", SADD `caddy:a:b:c:directory` "file.txt".
Root ancestor: nếu top-level component chưa có trong root set → SADD `{prefix}:__root__:directory` "topComponent".
Chỉ thêm basename vào parent set. Không tạo metadata cho directory — dir existence được infer từ directory set.

### Load(ctx, key)

Single command: `GET {prefix}:{key_colon}:binary`.
Return `fs.ErrNotExist` nếu key không tồn tại.

### Delete(ctx, key)

**Idempotent:** return `nil` nếu key không tồn tại. Return error chỉ nếu key vẫn tồn tại sau khi delete.

**Phase 1 — Atomic deletion (MULTI/EXEC):**

Single key deletion (file):
1. `DEL {prefix}:{key_colon}:binary`
2. `DEL {prefix}:{key_colon}:metadata`
3. `SREM {prefix}:{parent_colon}:directory {basename}`

Directory deletion (key là prefix của keys khác):
1. BFS/DFS qua directory sets, thu thập tất cả terminal keys
2. MULTI/EXEC: DEL tất cả `binary` + `metadata` + `directory` keys đã thu thập
3. SREM key từ parent directory set

Nếu cả binary và directory cùng tồn tại cho cùng key: xóa cả hai.

**Phase 2 — Best-effort ancestor pruning:**
- `SCARD {prefix}:{parent_colon}:directory` → nếu 0 → DEL directory set, SREM parent từ grandparent
- Tiếp tục lên ancestors cho đến khi gặp directory set còn có children hoặc tới root
- Prune là **opportunistic cleanup**, không nằm trong main transaction. Nếu prune fail hoặc race với concurrent write, chỉ để lại empty directory sets — không ảnh hưởng correctness. Concurrent `Store()` sẽ recreate ancestor sets khi cần.

### Exists(ctx, key)

`EXISTS {prefix}:{key_colon}:binary` → nếu true, return true.
Nếu false, check `EXISTS {prefix}:{key_colon}:directory` (directory case).
Return `false` nếu Redis error hoặc context cancellation. Interface `Exists() bool` không trả error.

### Stat(ctx, key)

**Main path:** `HGETALL {prefix}:{key_colon}:metadata` → map thành `KeyInfo`:
- `Key`: original key
- `Modified`: parse `mod` field (unix ms → time.Time)
- `Size`: parse `size` field
- `IsTerminal`: parse `terminal` field (1 = true)

**Directory fallback:** nếu metadata missing, check `EXISTS {prefix}:{key_colon}:directory`:
- Nếu directory set exists → `KeyInfo{Key: key, IsTerminal: false, Size: 0}`
- Nếu không → `fs.ErrNotExist`

**Corruption recovery:** nếu metadata missing nhưng binary exists (unexpected), derive `KeyInfo` từ binary GET. Đây là rare recovery path, không phải normal behavior.

### List(ctx, path, recursive)

**Non-recursive:** `SMEMBERS {prefix}:{path_colon}:directory` → join child names với path prefix.
- Trả về immediate children gồm cả file keys và directory keys.
- `path` bản thân nó không nằm trong output.
- Path rỗng (`""`) = root → dùng `{prefix}:__root__:directory`.

**Recursive:** DFS qua directory sets:
1. `SMEMBERS {prefix}:{path_colon}:directory`
2. Với mỗi child, kiểm tra child là directory: batch `EXISTS` cho tất cả children (pipelined reads)
3. Trả về tất cả descendants — gồm cả directory keys và terminal keys
4. Nếu child là directory → thêm child key vào output → recurse vào child
5. Respect ctx.Done()

**Edge cases:**
- `List(path)` trên path không tồn tại → `fs.ErrNotExist`
- `List(path)` trên path là file (không phải directory) → `fs.ErrNotExist`
- `List(path)` trên empty directory → `[]string{}`, nil
- Path rỗng → list từ root

## Error Handling

- `Load/Stat/List` key không tồn tại → return `fs.ErrNotExist`
- `Delete` key không tồn tại → return `nil` (idempotent, match FileStorage behavior)
- Redis connection lost → return error, để Caddy retry
- `ctx.Done()` → return context error ngay lập tức
- `Exists()` → return `false` on Redis error hoặc context cancellation
- `List()` empty directory → return `[]string{}`, nil
- `List()` path rỗng → list từ root

## Implementation Interfaces

```go
// certmagic.Storage
type RedisStorage struct {
    // Config
    URL             string `json:"url,omitempty"`
    Prefix          string `json:"prefix,omitempty"`
    LockTTL         string `json:"lock_ttl,omitempty"`
    LockRenewInterval string `json:"lock_renew_interval,omitempty"`

    // Internal
    client     *redis.Client
    locks      map[string]string
    mu         sync.Mutex
    logger     *zap.Logger

    // Parsed config
    lockTTL         time.Duration
    lockRenewInterval time.Duration
}
```

Implements:
- `certmagic.Storage` (Store/Load/Delete/Exists/List/Stat + Locker)
- `certmagic.TryLocker` (TryLock)
- `certmagic.LockLeaseRenewer` (RenewLockLease)
- `caddy.StorageConverter` (CertMagicStorage)
- `caddy.Module` (CaddyModule)
- `caddy.Provisioner` (Provision)

## Consistency Model

- **Single Redis instance, multiple Caddy nodes.**
- Writes: MULTI/EXEC transactions — binary + metadata + directory index commit atomically. Ancestor pruning là best-effort post-transaction cleanup.
- Reads: luôn thấy last-committed transaction state.
- Locks: lease-based mutual exclusion, owner token validated, Lua compare-and-delete/renew.
- No fencing tokens (CertMagic accepts rare mis-synchronizations).
- Equivalent or better consistency than default FileStorage.

## Deployment

- **Redis Standalone only.** Không support Cluster hay Sentinel.
- Nhiều Caddy nodes kết nối đến cùng 1 Redis instance.
- Config URL format: `redis://[user:password@]host:port/db` hoặc `rediss://` cho TLS.

## Dependencies

- `github.com/caddyserver/caddy` (v2)
- `github.com/caddyserver/certmagic`
- `github.com/redis/go-redis/v9`
- `go.uber.org/zap`
