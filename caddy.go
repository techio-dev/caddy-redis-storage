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
	caddy.RegisterModule(&RedisStorage{})
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
func (*RedisStorage) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "caddy.storage.redis",
		New: func() caddy.Module { return new(RedisStorage) },
	}
}

// Provision sets up the Redis client and validates config.
func (r *RedisStorage) Provision(ctx caddy.Context) error {
	r.logger = ctx.Logger()
	r.locks = make(map[string]string)

	// Resolve Caddy placeholders (e.g. {env.REDIS_URL})
	repl := caddy.NewReplacer()
	r.URL = repl.ReplaceAll(r.URL, "")
	r.Prefix = repl.ReplaceAll(r.Prefix, "")

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
	_ caddy.Module           = (*RedisStorage)(nil)
	_ caddy.Provisioner      = (*RedisStorage)(nil)
	_ caddy.StorageConverter = (*RedisStorage)(nil)
)
