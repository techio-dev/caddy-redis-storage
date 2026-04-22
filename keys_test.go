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
	if parent != "." {
		t.Errorf("splitPath parent = %q, want %q", parent, ".")
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
	if len(ancestors) != len(expected) {
		t.Fatalf("len(ancestorPaths) = %d, want %d", len(ancestors), len(expected))
	}
	for i, exp := range expected {
		if ancestors[i].dir != exp.dir || ancestors[i].base != exp.base {
			t.Errorf("ancestorPaths[%d] = {%q, %q}, want {%q, %q}",
				i, ancestors[i].dir, ancestors[i].base, exp.dir, exp.base)
		}
	}
}

func TestAncestorPathsSingleComponent(t *testing.T) {
	ancestors := ancestorPaths("file.txt")
	if len(ancestors) != 0 {
		t.Errorf("ancestorPaths single component = %d entries, want 0", len(ancestors))
	}
}
