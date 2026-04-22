package caddystorage

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
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

// Exists returns true if key exists as a file or directory.
func (r *RedisStorage) Exists(ctx context.Context, key string) bool {
	if ctx.Err() != nil {
		return false
	}

	exists, err := r.client.Exists(ctx, binaryKey(r.Prefix, key)).Result()
	if err != nil {
		return false
	}
	if exists > 0 {
		return true
	}

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

	// Directory fallback
	exists, err := r.client.Exists(ctx, directoryKey(r.Prefix, key)).Result()
	if err != nil {
		return certmagic.KeyInfo{}, fmt.Errorf("redis stat failed: %w", err)
	}
	if exists > 0 {
		return certmagic.KeyInfo{Key: key, IsTerminal: false, Size: 0}, nil
	}

	// Corruption recovery: binary exists without metadata
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

// Delete deletes key. Idempotent — returns nil if key does not exist.
func (r *RedisStorage) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	parent, base := splitPath(key)

	dirExists, _ := r.client.Exists(ctx, directoryKey(r.Prefix, key)).Result()
	binExists, _ := r.client.Exists(ctx, binaryKey(r.Prefix, key)).Result()

	if dirExists == 0 && binExists == 0 {
		return nil
	}

	// Phase 1: Atomic deletion
	if dirExists > 0 {
		allBinaries, allMetadatas, allDirs := r.collectSubtreeKeys(ctx, key)

		pipe := r.client.TxPipeline()
		for _, k := range allBinaries {
			pipe.Del(ctx, k)
		}
		for _, k := range allMetadatas {
			pipe.Del(ctx, k)
		}
		for _, k := range allDirs {
			pipe.Del(ctx, k)
		}
		if parent != key {
			pipe.SRem(ctx, directoryKey(r.Prefix, parent), base)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return fmt.Errorf("redis directory delete failed: %w", err)
		}
	}

	if binExists > 0 && dirExists == 0 {
		pipe := r.client.TxPipeline()
		pipe.Del(ctx, binaryKey(r.Prefix, key))
		pipe.Del(ctx, metadataKey(r.Prefix, key))
		pipe.SRem(ctx, directoryKey(r.Prefix, parent), base)
		if _, err := pipe.Exec(ctx); err != nil {
			return fmt.Errorf("redis delete failed: %w", err)
		}
	}

	if binExists > 0 && dirExists > 0 {
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

// pruneEmptyAncestors removes empty directory sets going upward. Best-effort.
func (r *RedisStorage) pruneEmptyAncestors(ctx context.Context, dirPath string) {
	current := dirPath
	for current != "" && current != "." {
		if ctx.Err() != nil {
			return
		}

		dirRedisKey := directoryKey(r.Prefix, current)
		count, err := r.client.SCard(ctx, dirRedisKey).Result()
		if err != nil {
			r.logger.Debug("ancestor prune: SCARD failed", zap.String("dir", current), zap.Error(err))
			return
		}

		if count > 0 {
			return
		}

		parent, base := splitPath(current)
		r.client.Del(ctx, dirRedisKey)
		if parent != current && parent != "." {
			r.client.SRem(ctx, directoryKey(r.Prefix, parent), base)
		}

		current = parent
	}
}

// List returns keys in the given path.
func (r *RedisStorage) List(ctx context.Context, p string, recursive bool) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	dirRedisKey := directoryKey(r.Prefix, p)

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

	keys := make([]string, 0, len(children))
	for _, child := range children {
		childPath := child
		if p != "" {
			childPath = p + "/" + child
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
			subKeys, err := r.List(ctx, keys[i], true)
			if err != nil {
				continue
			}
			allKeys = append(allKeys, subKeys...)
		}
	}

	return allKeys, nil
}

// Ensure interface compliance
var _ certmagic.Storage = (*RedisStorage)(nil)
