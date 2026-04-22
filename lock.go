package caddystorage

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/caddyserver/certmagic"
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

// RenewLockLease renews the TTL on a held lock.
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

// Interface guards for lock interfaces
var (
	_ certmagic.TryLocker        = (*RedisStorage)(nil)
	_ certmagic.LockLeaseRenewer = (*RedisStorage)(nil)
)
