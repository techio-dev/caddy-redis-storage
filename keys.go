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
func directoryKey(prefix, key string) string {
	if key == "" {
		return prefix + ":__root__:directory"
	}
	return prefix + ":" + toColonPath(key) + ":directory"
}

// lockKey returns the Redis key for a distributed lock.
func lockKey(prefix, name string) string {
	return prefix + ":locks:" + toColonPath(name)
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
		dir := ""
		if i > 0 {
			dir = strings.Join(parts[:i], "/")
		}
		entries = append(entries, ancestorEntry{dir: dir, base: parts[i]})
	}
	return entries
}
