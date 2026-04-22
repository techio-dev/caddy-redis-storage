# Caddy Redis Storage Plugin — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `certmagic.Storage` backend using Redis cho Caddy cluster HA.

**Architecture:** Caddy plugin registered as `caddy.storage.redis`. Redis stores binary data (String), metadata (Hash), directory index (Set). MULTI/EXEC transactions cho atomic writes. Distributed locking via owner token + Lua compare-and-delete/renew.

**Tech Stack:** Go, github.com/caddyserver/caddy/v2, github.com/caddyserver/certmagic, github.com/redis/go-redis/v9

**Spec:** `docs/superpowers/specs/2026-04-23-redis-storage-design.md`

---

### Task 1: Project Scaffold

**Files:**
- Create: `go.mod`

- [ ] **Step 1: Initialize Go module**

```bash
cd /Users/naicoi/Projects/caddy-redis-storage
go mod init github.com/naicoi/caddy-redis-storage
```

- [ ] **Step 2: Add dependencies**

```bash
go get github.com/caddyserver/caddy/v2@latest
go get github.com/caddyserver/certmagic@latest
go get github.com/redis/go-redis/v9@latest
go get go.uber.org/zap@latest
go get github.com/google/uuid@latest
```

- [ ] **Step 3: Create empty source files**

```bash
touch caddy.go storage.go lock.go keys.go caddy_test.go
```

- [ ] **Step 4: Verify build**

```bash
go build ./...
```

Expected: no errors (empty packages compile).

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "chore: initialize project scaffold with dependencies"
```

---

### Task 2: Key Schema Helpers

**Files:**
- Create: `keys.go`
- Create: `keys_test.go`

- [ ] **Step 1: Write tests for key helpers**

```go
// keys_test.go
package caddystorage

import "testing"

func TestToColonPath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"certificates/acme/example.com", "certificates:acme:example.com"},
		{"a/b/c", "a:b:c"},
		{"single", "single"},
		{"", ""},
	}
	for _, tt := range tests {
		got := toColonPath(tt.input)
		if got != tt.expected {
			t.Errorf("toColonPath(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestBinaryKey(t *testing.T) {
	got := binaryKey("caddy", "certificates/acme/example.com/example.com.crt")
	want := "caddy:certificates:acme:example.com:example.com.crt:binary"
	if got != want {
		t.Errorf("binaryKey() = %q, want %q", got, want)
	}
}

func TestMetadataKey(t *testing.T) {
	got := metadataKey("caddy", "certificates/acme/example.com/example.com.crt")
	want := "caddy:certificates:acme:example.com:example.com.crt:metadata"
	if got != want {
		t.Errorf("metadataKey() = %q, want %q", got, want)
	}
}

func TestDirectoryKey(t *testing.T) {
	got := directoryKey("caddy", "certificates/acme/example.com")
	want := "caddy:certificates:acme:example.com:directory"
	if got != want {
		t.Errorf("directoryKey() = %q, want %q", got, want)
	}
}

func TestDirectoryKeyRoot(t *testing.T) {
	got := directoryKey("caddy", "")
	want := "caddy:__root__:directory"
	if got != want {
		t.Errorf("directoryKey('') = %q, want %q", got, want)
	}
}

func TestLockKey(t *testing.T) {
	got := lockKey("caddy", "certname")
	want := "caddy:certname:lock"
	if got != want {
		t.Errorf("lockKey() = %q, want %q", got, want)
	}
}

func TestSplitPath(t *testing.T) {
	parent, base := splitPath("a/b/c/file.txt")
	if parent != "a/b/c" {
		t.Errorf("splitPath parent = %q, want %q", parent, "a/b/c")
	}
	if base != "file.txt" {
		t.Errorf("splitPath base = %q, want %q", base, "file.txt")
	}
}

func TestSplitPathSingle(t *testing.T) {
	parent, base := splitPath("file.txt")
	if parent != "" {
		t.Errorf("splitPath parent = %q, want empty", parent)
	}
	if base != "file.txt" {
		t.Errorf("splitPath base = %q, want %q", base, "file.txt")
	}
}

func TestAncestorPaths(t *testing.T) {
	ancestors := ancestorPaths("a/b/c/file.txt")
	expected := []struct{ dir, base string }{
		{"", "a"},
		{"a", "b"},
		{"a/b", "c"},
	}
	for i, exp := range expected {
		if ancestors[i].dir != exp.dir || ancestors[i].base != exp.base {
			t.Errorf("ancestorPaths[%d] = {%q, %q}, want {%q, %q}",
				i, ancestors[i].dir, ancestors[i].base, exp.dir, exp.base)
		}
	}
	if len(ancestors) != 3 {
		t.Errorf("len(ancestorPaths) = %d, want 3", len(ancestors))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -run "TestToColonPath|TestBinaryKey|TestMetadataKey|TestDirectoryKey|TestLockKey|TestSplitPath|TestAncestorPaths" -v
```

Expected: compilation errors — functions not defined.

- [ ] **Step 3: Implement key helpers**

```go
// keys.go
package caddystorage

import (
	"path"
	"strings"
)

// toColonPath converts a certmagic storage key (/ separated) to Redis colon-separated path.
func toColonPath(key string) string {
	return strings.ReplaceAll(key, "/", ":")
}

// binaryKey returns the Redis key for storing file binary data.
func binaryKey(prefix, key string) string {
	return prefix + ":" + toColonPath(key) + ":binary"
}

// metadataKey returns the Redis key for storing file metadata hash.
func metadataKey(prefix, key string) string {
	return prefix + ":" + toColonPath(key) + ":metadata"
}

// directoryKey returns the Redis key for the directory set of child names.
// Empty path returns the root directory key.
func directoryKey(prefix, path string) string {
	if path == "" {
		return prefix + ":__root__:directory"
	}
	return prefix + ":" + toColonPath(path) + ":directory"
}

// lockKey returns the Redis key for a distributed lock.
func lockKey(prefix, name string) string {
	return prefix + ":" + toColonPath(name) + ":lock"
}

// splitPath returns parent directory and basename from a storage key.
func splitPath(key string) (parent, base string) {
	return path.Dir(key), path.Base(key)
}

// ancestorEntry represents a directory-to-child relationship for ancestor indexing.
type ancestorEntry struct {
	dir  string // parent directory path (empty for root)
	base string // child name to add to parent directory set
}

// ancestorPaths returns all ancestor directory entries that need to be updated
// when storing a key. For key "a/b/c/file.txt" it returns entries for
// root→a, a→b, a/b→c.
func ancestorPaths(key string) []ancestorEntry {
	parts := strings.Split(key, "/")
	if len(parts) <= 1 {
		return nil
	}
	entries := make([]ancestorEntry, 0, len(parts)-1)
	for i := 0; i < len(parts)-1; i++ {
		dir := strings.Join(parts[:i], "/")
		if i == 0 {
			dir = ""
		}
		entries = append(entries, ancestorEntry{dir: dir, base: parts[i]})
	}
	return entries
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test -run "TestToColonPath|TestBinaryKey|TestMetadataKey|TestDirectoryKey|TestLockKey|TestSplitPath|TestAncestorPaths" -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add keys.go keys_test.go && git commit -m "feat: add Redis key schema helpers"
```

---

### Task 3: Module Registration + RedisStorage Struct

**Files:**
- Create: `caddy.go`

- [ ] **Step 1: Write module registration and struct**

```go
// caddy.go
package caddystorage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/certmagic"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(RedisStorage{})
}

// RedisStorage implements certmagic.Storage backed by Redis.
type RedisStorage struct {
	// Configuration (JSON)
	URL               string `json:"url,omitempty"`
	Prefix            string `json:"prefix,omitempty"`
	LockTTL           string `json:"lock_ttl,omitempty"`
	LockRenewInterval string `json:"lock_renew_interval,omitempty"`

	// Internal state
	client *redis.Client
	locks  map[string]string
	mu     sync.Mutex
	logger *zap.Logger

	// Parsed config
	lockTTL           time.Duration
	lockRenewInterval time.Duration
}

// CaddyModule returns the Caddy module info.
func (RedisStorage) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "caddy.storage.redis",
		New: func() caddy.Module { return new(RedisStorage) },
	}
}

// Provision sets up the Redis client and validates config.
func (r *RedisStorage) Provision(ctx caddy.Context) error {
	r.logger = ctx.Logger()
	r.locks = make(map[string]string)

	// Defaults
	if r.Prefix == "" {
		r.Prefix = "caddy"
	}
	if r.URL == "" {
		r.URL = "redis://localhost:6379/0"
	}

	// Parse lock TTL
	r.lockTTL = 60 * time.Second
	if r.LockTTL != "" {
		d, err := time.ParseDuration(r.LockTTL)
		if err != nil {
			return fmt.Errorf("invalid lock_ttl: %w", err)
		}
		r.lockTTL = d
	}

	// Parse lock renew interval
	r.lockRenewInterval = 20 * time.Second
	if r.LockRenewInterval != "" {
		d, err := time.ParseDuration(r.LockRenewInterval)
		if err != nil {
			return fmt.Errorf("invalid lock_renew_interval: %w", err)
		}
		r.lockRenewInterval = d
	}

	// Validate
	if r.lockRenewInterval >= r.lockTTL {
		return fmt.Errorf("lock_renew_interval (%s) must be less than lock_ttl (%s)", r.lockRenewInterval, r.lockTTL)
	}
	if r.lockTTL <= 0 || r.lockRenewInterval <= 0 {
		return fmt.Errorf("lock_ttl and lock_renew_interval must be positive")
	}

	// Create Redis client
	opts, err := redis.ParseURL(r.URL)
	if err != nil {
		return fmt.Errorf("invalid redis URL: %w", err)
	}
	r.client = redis.NewClient(opts)

	// Verify connection
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.client.Ping(pingCtx).Err(); err != nil {
		return fmt.Errorf("redis connection failed: %w", err)
	}

	r.logger.Info("redis storage connected",
		zap.String("url", r.URL),
		zap.String("prefix", r.Prefix),
		zap.Duration("lock_ttl", r.lockTTL),
		zap.Duration("lock_renew_interval", r.lockRenewInterval),
	)

	return nil
}

// CertMagicStorage returns the certmagic.Storage implementation.
func (r *RedisStorage) CertMagicStorage() (certmagic.Storage, error) {
	return r, nil
}

// Interface guards
var (
	_ caddy.Module          = (*RedisStorage)(nil)
	_ caddy.Provisioner     = (*RedisStorage)(nil)
	_ caddy.StorageConverter = (*RedisStorage)(nil)
)
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

Expected: compiles without errors.

- [ ] **Step 3: Commit**

```bash
git add caddy.go && git commit -m "feat: add Caddy module registration and RedisStorage struct"
```

---

### Task 4: Store + Load

**Files:**
- Create: `storage.go`

- [ ] **Step 1: Implement Store and Load**

```go
// storage.go
package caddystorage

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/redis/go-redis/v9"
)

// Store saves value at key using MULTI/EXEC transaction.
func (r *RedisStorage) Store(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	now := time.Now().UnixMilli()
	_, base := splitPath(key)
	ancestors := ancestorPaths(key)

	pipe := r.client.TxPipeline()

	// 1. Set binary data
	pipe.Set(ctx, binaryKey(r.Prefix, key), value, 0)

	// 2. Set metadata hash
	pipe.HSet(ctx, metadataKey(r.Prefix, key),
		"mod", now,
		"size", len(value),
		"terminal", 1,
	)

	// 3. Add to parent directory set
	pipe.SAdd(ctx, directoryKey(r.Prefix, path.Dir(key)), base)

	// 4. Add ancestor directory entries
	for _, entry := range ancestors {
		pipe.SAdd(ctx, directoryKey(r.Prefix, entry.dir), entry.base)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis store transaction failed: %w", err)
	}

	return nil
}

// Load retrieves the value at key.
func (r *RedisStorage) Load(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	val, err := r.client.Get(ctx, binaryKey(r.Prefix, key)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, fs.ErrNotExist
		}
		return nil, fmt.Errorf("redis load failed: %w", err)
	}

	return val, nil
}

// Ensure interface compliance at compile time.
var _ certmagic.Storage = (*RedisStorage)(nil)
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

Expected: compiles (note: certmagic.Storage requires all methods — this will fail until all methods are implemented. Temporarily comment out the interface guard and proceed).

- [ ] **Step 3: Commit**

```bash
git add storage.go && git commit -m "feat: implement Store and Load methods"
```

---

### Task 5: Exists + Stat

**Files:**
- Modify: `storage.go`

- [ ] **Step 1: Implement Exists and Stat**

Add to `storage.go`:

```go
// Exists returns true if key exists as a file or directory.
func (r *RedisStorage) Exists(ctx context.Context, key string) bool {
	if ctx.Err() != nil {
		return false
	}

	// Check file binary
	exists, err := r.client.Exists(ctx, binaryKey(r.Prefix, key)).Result()
	if err != nil {
		return false
	}
	if exists > 0 {
		return true
	}

	// Check directory set
	exists, err = r.client.Exists(ctx, directoryKey(r.Prefix, key)).Result()
	if err != nil {
		return false
	}
	return exists > 0
}

// Stat returns key info.
func (r *RedisStorage) Stat(ctx context.Context, key string) (certmagic.KeyInfo, error) {
	if err := ctx.Err(); err != nil {
		return certmagic.KeyInfo{}, err
	}

	// Main path: read metadata hash
	meta, err := r.client.HGetAll(ctx, metadataKey(r.Prefix, key)).Result()
	if err != nil {
		return certmagic.KeyInfo{}, fmt.Errorf("redis stat failed: %w", err)
	}

	if len(meta) > 0 {
		ki := certmagic.KeyInfo{Key: key}
		if v, ok := meta["mod"]; ok {
			var ms int64
			fmt.Sscanf(v, "%d", &ms)
			ki.Modified = time.UnixMilli(ms)
		}
		if v, ok := meta["size"]; ok {
			fmt.Sscanf(v, "%d", &ki.Size)
		}
		if v, ok := meta["terminal"]; ok {
			ki.IsTerminal = v == "1"
		}
		return ki, nil
	}

	// Directory fallback: check directory set existence
	exists, err := r.client.Exists(ctx, directoryKey(r.Prefix, key)).Result()
	if err != nil {
		return certmagic.KeyInfo{}, fmt.Errorf("redis stat failed: %w", err)
	}
	if exists > 0 {
		return certmagic.KeyInfo{Key: key, IsTerminal: false, Size: 0}, nil
	}

	// Corruption recovery: check if binary exists without metadata
	exists, err = r.client.Exists(ctx, binaryKey(r.Prefix, key)).Result()
	if err != nil {
		return certmagic.KeyInfo{}, fmt.Errorf("redis stat failed: %w", err)
	}
	if exists > 0 {
		data, err := r.client.Get(ctx, binaryKey(r.Prefix, key)).Bytes()
		if err != nil {
			return certmagic.KeyInfo{}, fmt.Errorf("redis stat fallback failed: %w", err)
		}
		return certmagic.KeyInfo{Key: key, IsTerminal: true, Size: int64(len(data))}, nil
	}

	return certmagic.KeyInfo{}, fs.ErrNotExist
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add storage.go && git commit -m "feat: implement Exists and Stat methods"
```

---

### Task 6: Delete

**Files:**
- Modify: `storage.go`

- [ ] **Step 1: Implement Delete**

Add to `storage.go`:

```go
// Delete deletes key. Idempotent — returns nil if key does not exist.
// For directories, deletes entire subtree.
func (r *RedisStorage) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	parent, base := splitPath(key)

	// Check if it's a directory (has children)
	dirExists, _ := r.client.Exists(ctx, directoryKey(r.Prefix, key)).Result()
	binExists, _ := r.client.Exists(ctx, binaryKey(r.Prefix, key)).Result()

	if dirExists == 0 && binExists == 0 {
		// Nothing to delete — idempotent
		return nil
	}

	// Phase 1: Atomic deletion
	if dirExists > 0 {
		// Directory deletion: collect subtree keys, then transaction delete
		allBinaryKeys, allMetadataKeys, allDirKeys := r.collectSubtreeKeys(ctx, key)

		pipe := r.client.TxPipeline()
		for _, k := range allBinaryKeys {
			pipe.Del(ctx, k)
		}
		for _, k := range allMetadataKeys {
			pipe.Del(ctx, k)
		}
		for _, k := range allDirKeys {
			pipe.Del(ctx, k)
		}
		// Remove self from parent directory set
		if parent != key {
			pipe.SRem(ctx, directoryKey(r.Prefix, parent), base)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return fmt.Errorf("redis directory delete failed: %w", err)
		}
	}

	if binExists > 0 && dirExists == 0 {
		// Single file deletion
		pipe := r.client.TxPipeline()
		pipe.Del(ctx, binaryKey(r.Prefix, key))
		pipe.Del(ctx, metadataKey(r.Prefix, key))
		pipe.SRem(ctx, directoryKey(r.Prefix, parent), base)
		if _, err := pipe.Exec(ctx); err != nil {
			return fmt.Errorf("redis delete failed: %w", err)
		}
	}

	if binExists > 0 && dirExists > 0 {
		// Corrupt state: both file and directory exist
		pipe := r.client.TxPipeline()
		pipe.Del(ctx, binaryKey(r.Prefix, key))
		pipe.Del(ctx, metadataKey(r.Prefix, key))
		pipe.Del(ctx, directoryKey(r.Prefix, key))
		pipe.SRem(ctx, directoryKey(r.Prefix, parent), base)
		if _, err := pipe.Exec(ctx); err != nil {
			return fmt.Errorf("redis delete (corrupt state) failed: %w", err)
		}
	}

	// Phase 2: Best-effort ancestor pruning
	r.pruneEmptyAncestors(ctx, parent)

	return nil
}

// collectSubtreeKeys traverses directory sets to collect all keys in a subtree.
func (r *RedisStorage) collectSubtreeKeys(ctx context.Context, rootKey string) (binaries, metadatas, directories []string) {
	queue := []string{rootKey}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		dirRedisKey := directoryKey(r.Prefix, current)
		directories = append(directories, dirRedisKey)

		children, err := r.client.SMembers(ctx, dirRedisKey).Result()
		if err != nil {
			continue
		}

		for _, child := range children {
			childPath := current + "/" + child
			if current == "" {
				childPath = child
			}

			// Check if child is a directory
			childDirExists, _ := r.client.Exists(ctx, directoryKey(r.Prefix, childPath)).Result()
			if childDirExists > 0 {
				queue = append(queue, childPath)
			}

			binaries = append(binaries, binaryKey(r.Prefix, childPath))
			metadatas = append(metadatas, metadataKey(r.Prefix, childPath))
		}

		if ctx.Err() != nil {
			return
		}
	}

	return
}

// pruneEmptyAncestors removes empty directory sets going upward.
// Best-effort — errors are logged but not returned.
func (r *RedisStorage) pruneEmptyAncestors(ctx context.Context, dirPath string) {
	current := dirPath
	for current != "" {
		if ctx.Err() != nil {
			return
		}

		dirRedisKey := directoryKey(r.Prefix, current)
		count, err := r.client.SCard(ctx, dirRedisKey).Result()
		if err != nil {
			r.logger.Debug("ancestor prune: SCARD failed", zap.String("dir", dirPath), zap.Error(err))
			return
		}

		if count > 0 {
			return // directory still has children, stop pruning
		}

		// Directory is empty — delete it and remove from parent
		parent, base := splitPath(current)
		r.client.Del(ctx, dirRedisKey)
		if parent != current {
			r.client.SRem(ctx, directoryKey(r.Prefix, parent), base)
		}

		current = parent
	}
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add storage.go && git commit -m "feat: implement Delete with subtree traversal and ancestor pruning"
```

---

### Task 7: List

**Files:**
- Modify: `storage.go`

- [ ] **Step 1: Implement List**

Add to `storage.go`:

```go
// List returns keys in the given path.
// Non-recursive: immediate children only.
// Recursive: all descendants including directory keys.
func (r *RedisStorage) List(ctx context.Context, path string, recursive bool) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	dirRedisKey := directoryKey(r.Prefix, path)

	// Check directory exists
	exists, err := r.client.Exists(ctx, dirRedisKey).Result()
	if err != nil {
		return nil, fmt.Errorf("redis list failed: %w", err)
	}
	if exists == 0 {
		return nil, fs.ErrNotExist
	}

	children, err := r.client.SMembers(ctx, dirRedisKey).Result()
	if err != nil {
		return nil, fmt.Errorf("redis list failed: %w", err)
	}

	if len(children) == 0 {
		return []string{}, nil
	}

	// Build full keys
	keys := make([]string, 0, len(children))
	for _, child := range children {
		childPath := child
		if path != "" {
			childPath = path + "/" + child
		}
		keys = append(keys, childPath)
	}

	if !recursive {
		return keys, nil
	}

	// Recursive: DFS collecting all descendants
	var allKeys []string
	allKeys = append(allKeys, keys...)

	// Batch check which children are directories
	dirCheckPipe := r.client.Pipeline()
	cmds := make([]*redis.IntCmd, len(keys))
	for i, k := range keys {
		cmds[i] = dirCheckPipe.Exists(ctx, directoryKey(r.Prefix, k))
	}
	if _, err := dirCheckPipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("redis list directory check failed: %w", err)
	}

	for i, cmd := range cmds {
		if cmd.Val() > 0 {
			// Child is a directory — recurse
			subKeys, err := r.List(ctx, keys[i], true)
			if err != nil {
				continue // best effort
			}
			allKeys = append(allKeys, subKeys...)
		}
	}

	return allKeys, nil
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add storage.go && git commit -m "feat: implement List with recursive DFS via directory sets"
```

---

### Task 8: Lock + Unlock + TryLock

**Files:**
- Create: `lock.go`

- [ ] **Step 1: Implement Lock, Unlock, TryLock**

```go
// lock.go
package caddystorage

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// unlockScript is a Lua script that deletes a lock key only if the value matches the owner token.
var unlockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end
`)

// renewScript is a Lua script that renews a lock TTL only if the value matches the owner token.
var renewScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("PEXPIRE", KEYS[1], ARGV[2])
else
    return 0
end
`)

// Lock acquires a distributed lock, blocking until acquired or error.
func (r *RedisStorage) Lock(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	lockRedisKey := lockKey(r.Prefix, name)
	ttlMs := r.lockTTL.Milliseconds()

	// Base backoff: 500ms, cap: 2s
	backoff := 500 * time.Millisecond
	maxBackoff := 2 * time.Second

	for {
		token := uuid.New().String()
		ok, err := r.client.SetNX(ctx, lockRedisKey, token, r.lockTTL).Result()
		if err != nil {
			return fmt.Errorf("redis lock failed: %w", err)
		}
		if ok {
			r.mu.Lock()
			r.locks[name] = token
			r.mu.Unlock()
			return nil
		}

		// Wait with backoff + jitter
		jitter := time.Duration(rand.Int64N(int64(400*time.Millisecond))) - 200*time.Millisecond
		wait := backoff + jitter
		if wait < 0 {
			wait = 0
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}

		if backoff < maxBackoff {
			backoff = backoff * 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// Unlock releases the named lock. Only succeeds if this instance holds the lock.
func (r *RedisStorage) Unlock(ctx context.Context, name string) error {
	r.mu.Lock()
	token, ok := r.locks[name]
	if !ok {
		r.mu.Unlock()
		return errors.New("lock not held by this instance: " + name)
	}
	delete(r.locks, name)
	r.mu.Unlock()

	lockRedisKey := lockKey(r.Prefix, name)
	result, err := unlockScript.Run(ctx, r.client, []string{lockRedisKey}, token).Int()
	if err != nil {
		return fmt.Errorf("redis unlock failed: %w", err)
	}
	if result == 0 {
		return errors.New("lock already released or token mismatch: " + name)
	}

	return nil
}

// TryLock attempts to acquire the lock without blocking.
func (r *RedisStorage) TryLock(ctx context.Context, name string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	token := uuid.New().String()
	lockRedisKey := lockKey(r.Prefix, name)

	ok, err := r.client.SetNX(ctx, lockRedisKey, token, r.lockTTL).Result()
	if err != nil {
		return false, fmt.Errorf("redis trylock failed: %w", err)
	}
	if ok {
		r.mu.Lock()
		r.locks[name] = token
		r.mu.Unlock()
		return true, nil
	}

	return false, nil
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add lock.go && git commit -m "feat: implement Lock, Unlock, TryLock with Lua scripts"
```

---

### Task 9: RenewLockLease

**Files:**
- Modify: `lock.go`

- [ ] **Step 1: Implement RenewLockLease**

Add to `lock.go`:

```go
// RenewLockLease renews the TTL on a held lock.
func (r *RedisStorage) RenewLockLease(ctx context.Context, lockKey string, leaseDuration time.Duration) error {
	r.mu.Lock()
	token, ok := r.locks[lockKey]
	if !ok {
		r.mu.Unlock()
		return errors.New("lock not held by this instance: " + lockKey)
	}
	r.mu.Unlock()

	redisKey := lockKeyFunc(r.Prefix, lockKey)
	result, err := renewScript.Run(ctx, r.client, []string{redisKey}, token, leaseDuration.Milliseconds()).Int()
	if err != nil {
		return fmt.Errorf("redis renew lock failed: %w", err)
	}
	if result == 0 {
		return errors.New("lock renewal failed — token mismatch or lock no longer held: " + lockKey)
	}

	return nil
}
```

Note: need to add a `lockKeyFunc` alias to avoid name collision with the `lockKey` function. Add at top of `lock.go`:

```go
// lockKeyFunc is an alias for lockKey to avoid collision with method receiver names.
var lockKeyFunc = lockKey
```

Actually, simpler — just call `lockKey()` directly since there's no name collision in Go (method receiver `r` vs package-level function). Replace `lockKeyFunc` with `lockKey`:

```go
	redisKey := lockKey(r.Prefix, lockKey)
```

Wait — the parameter `lockKey` shadows the function name. Rename the parameter:

```go
func (r *RedisStorage) RenewLockLease(ctx context.Context, name string, leaseDuration time.Duration) error {
	r.mu.Lock()
	token, ok := r.locks[name]
	if !ok {
		r.mu.Unlock()
		return errors.New("lock not held by this instance: " + name)
	}
	r.mu.Unlock()

	redisKey := lockKey(r.Prefix, name)
	result, err := renewScript.Run(ctx, r.client, []string{redisKey}, token, leaseDuration.Milliseconds()).Int()
	if err != nil {
		return fmt.Errorf("redis renew lock failed: %w", err)
	}
	if result == 0 {
		return errors.New("lock renewal failed — token mismatch or lock no longer held: " + name)
	}

	return nil
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add lock.go && git commit -m "feat: implement RenewLockLease"
```

---

### Task 10: Integration Tests

**Files:**
- Create: `caddy_test.go`

Integration tests require a running Redis instance at `localhost:6379`. Tests use a unique prefix per test run to avoid collisions.

- [ ] **Step 1: Write integration test helpers and Store/Load tests**

```go
// caddy_test.go
package caddystorage

import (
	"context"
	"fmt"
	"io/fs"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newTestStorage(t *testing.T) *RedisStorage {
	t.Helper()
	prefix := fmt.Sprintf("test:%d", time.Now().UnixNano())
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available at localhost:6379")
	}

	storage := &RedisStorage{
		Prefix:            prefix,
		client:            client,
		locks:             make(map[string]string),
		lockTTL:           5 * time.Second,
		lockRenewInterval: 1 * time.Second,
	}

	t.Cleanup(func() {
		// Cleanup test keys
		iter := client.Scan(context.Background(), 0, prefix+":*", 0).Iterator()
		var keys []string
		for iter.Next(context.Background()) {
			keys = append(keys, iter.Val())
		}
		if len(keys) > 0 {
			client.Del(context.Background(), keys...)
		}
		client.Close()
	})

	return storage
}

func TestStoreAndLoad(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	// Store
	err := s.Store(ctx, "certificates/acme/example.com/example.com.crt", []byte("cert-data"))
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Load
	data, err := s.Load(ctx, "certificates/acme/example.com/example.com.crt")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if string(data) != "cert-data" {
		t.Errorf("Load got %q, want %q", data, "cert-data")
	}

	// Load non-existent
	_, err = s.Load(ctx, "nonexistent")
	if !errors_Is(err, fs.ErrNotExist) {
		t.Errorf("Load nonexistent got err=%v, want fs.ErrNotExist", err)
	}
}

func TestExists(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	s.Store(ctx, "a/b/c.txt", []byte("data"))

	if !s.Exists(ctx, "a/b/c.txt") {
		t.Error("Exists should return true for stored file")
	}
	if !s.Exists(ctx, "a/b") {
		t.Error("Exists should return true for parent directory")
	}
	if !s.Exists(ctx, "a") {
		t.Error("Exists should return true for ancestor directory")
	}
	if s.Exists(ctx, "nonexistent") {
		t.Error("Exists should return false for non-existent key")
	}
}

func TestStat(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	s.Store(ctx, "test/file.txt", []byte("hello world"))

	info, err := s.Stat(ctx, "test/file.txt")
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if info.Key != "test/file.txt" {
		t.Errorf("Stat Key = %q, want %q", info.Key, "test/file.txt")
	}
	if !info.IsTerminal {
		t.Error("Stat IsTerminal should be true for file")
	}
	if info.Size != 11 {
		t.Errorf("Stat Size = %d, want 11", info.Size)
	}
	if info.Modified.IsZero() {
		t.Error("Stat Modified should not be zero")
	}

	// Directory stat
	info, err = s.Stat(ctx, "test")
	if err != nil {
		t.Fatalf("Stat directory failed: %v", err)
	}
	if info.IsTerminal {
		t.Error("Stat IsTerminal should be false for directory")
	}

	// Non-existent
	_, err = s.Stat(ctx, "nonexistent")
	if !errors_Is(err, fs.ErrNotExist) {
		t.Errorf("Stat nonexistent got err=%v, want fs.ErrNotExist", err)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	// Store a file
	s.Store(ctx, "a/b/c.txt", []byte("data"))

	// Delete file
	err := s.Delete(ctx, "a/b/c.txt")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify gone
	if s.Exists(ctx, "a/b/c.txt") {
		t.Error("File should be deleted")
	}

	// Delete non-existent (idempotent)
	err = s.Delete(ctx, "nonexistent")
	if err != nil {
		t.Errorf("Delete non-existent should return nil, got %v", err)
	}
}

func TestDeleteDirectory(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	// Store multiple files in same directory
	s.Store(ctx, "dir/file1.txt", []byte("data1"))
	s.Store(ctx, "dir/file2.txt", []byte("data2"))

	// Delete directory
	err := s.Delete(ctx, "dir")
	if err != nil {
		t.Fatalf("Delete directory failed: %v", err)
	}

	if s.Exists(ctx, "dir") {
		t.Error("Directory should be deleted")
	}
	if s.Exists(ctx, "dir/file1.txt") {
		t.Error("file1 should be deleted")
	}
	if s.Exists(ctx, "dir/file2.txt") {
		t.Error("file2 should be deleted")
	}
}

func TestList(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	s.Store(ctx, "dir/file1.txt", []byte("d1"))
	s.Store(ctx, "dir/file2.txt", []byte("d2"))
	s.Store(ctx, "dir/sub/file3.txt", []byte("d3"))

	// Non-recursive
	keys, err := s.List(ctx, "dir", false)
	if err != nil {
		t.Fatalf("List non-recursive failed: %v", err)
	}
	if len(keys) != 3 {
		t.Errorf("List non-recursive got %d keys, want 3: %v", len(keys), keys)
	}

	// Recursive
	keys, err = s.List(ctx, "dir", true)
	if err != nil {
		t.Fatalf("List recursive failed: %v", err)
	}
	if len(keys) != 4 {
		t.Errorf("List recursive got %d keys, want 4 (file1, file2, sub, sub/file3): %v", len(keys), keys)
	}
}

func TestLockUnlock(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	err := s.Lock(ctx, "testlock")
	if err != nil {
		t.Fatalf("Lock failed: %v", err)
	}

	err = s.Unlock(ctx, "testlock")
	if err != nil {
		t.Fatalf("Unlock failed: %v", err)
	}

	// Unlock again should fail
	err = s.Unlock(ctx, "testlock")
	if err == nil {
		t.Error("Double unlock should fail")
	}
}

func TestTryLock(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	ok, err := s.TryLock(ctx, "testlock")
	if err != nil {
		t.Fatalf("TryLock failed: %v", err)
	}
	if !ok {
		t.Error("TryLock should succeed on first attempt")
	}

	// Second TryLock should fail
	ok, err = s.TryLock(ctx, "testlock")
	if err != nil {
		t.Fatalf("Second TryLock failed: %v", err)
	}
	if ok {
		t.Error("Second TryLock should fail — lock already held")
	}

	s.Unlock(ctx, "testlock")
}

func TestRenewLockLease(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	s.Lock(ctx, "testlock")

	err := s.RenewLockLease(ctx, "testlock", 10*time.Second)
	if err != nil {
		t.Fatalf("RenewLockLease failed: %v", err)
	}

	s.Unlock(ctx, "testlock")

	// Renew non-held lock
	err = s.RenewLockLease(ctx, "testlock", 10*time.Second)
	if err == nil {
		t.Error("RenewLockLease on non-held lock should fail")
	}
}
```

Note: needs `import "errors"` and `errors.Is` — add to imports.

- [ ] **Step 2: Run tests (requires Redis)**

```bash
docker run -d --name test-redis -p 6379:6379 redis:7-alpine
go test -v -timeout 30s
```

Expected: all PASS.

- [ ] **Step 3: Commit**

```bash
git add caddy_test.go && git commit -m "test: add integration tests for all storage and lock methods"
```

---

### Task 11: Verify Full Build as Caddy Module

**Files:**
- None (verification only)

- [ ] **Step 1: Verify interface compliance**

```bash
go build ./...
```

Expected: no errors. The `var _ certmagic.Storage = (*RedisStorage)(nil)` guard confirms full interface compliance.

- [ ] **Step 2: Verify xcaddy build (optional)**

```bash
go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
xcaddy build --with github.com/naicoi/caddy-redis-storage=.
./caddy list-modules | grep redis
```

Expected: `caddy.storage.redis` appears in module list.

- [ ] **Step 3: Final commit (if any fixes needed)**

```bash
git add -A && git commit -m "chore: verify full caddy module build"
```
