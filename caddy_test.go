package caddystorage

import (
	"context"
	"errors"
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

	err := s.Store(ctx, "certificates/acme/example.com/example.com.crt", []byte("cert-data"))
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	data, err := s.Load(ctx, "certificates/acme/example.com/example.com.crt")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if string(data) != "cert-data" {
		t.Errorf("Load got %q, want %q", data, "cert-data")
	}

	_, err = s.Load(ctx, "nonexistent")
	if !errors.Is(err, fs.ErrNotExist) {
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

	info, err = s.Stat(ctx, "test")
	if err != nil {
		t.Fatalf("Stat directory failed: %v", err)
	}
	if info.IsTerminal {
		t.Error("Stat IsTerminal should be false for directory")
	}

	_, err = s.Stat(ctx, "nonexistent")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat nonexistent got err=%v, want fs.ErrNotExist", err)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	s.Store(ctx, "a/b/c.txt", []byte("data"))

	err := s.Delete(ctx, "a/b/c.txt")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if s.Exists(ctx, "a/b/c.txt") {
		t.Error("File should be deleted")
	}

	err = s.Delete(ctx, "nonexistent")
	if err != nil {
		t.Errorf("Delete non-existent should return nil, got %v", err)
	}
}

func TestDeleteDirectory(t *testing.T) {
	s := newTestStorage(t)
	ctx := context.Background()

	s.Store(ctx, "dir/file1.txt", []byte("data1"))
	s.Store(ctx, "dir/file2.txt", []byte("data2"))

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

	keys, err := s.List(ctx, "dir", false)
	if err != nil {
		t.Fatalf("List non-recursive failed: %v", err)
	}
	if len(keys) != 3 {
		t.Errorf("List non-recursive got %d keys, want 3: %v", len(keys), keys)
	}

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

	err = s.RenewLockLease(ctx, "testlock", 10*time.Second)
	if err == nil {
		t.Error("RenewLockLease on non-held lock should fail")
	}
}
