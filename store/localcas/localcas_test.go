package localcas

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/domain"
)

func TestPutGetRoundtrip(t *testing.T) {
	s := New(t.TempDir())
	ctx := context.Background()

	content := []byte("# A Plan\n\nSome content with unicode — arrows → and §.\n")
	h, err := s.Put(ctx, content)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(string(h)) {
		t.Fatalf("hash %q is not hex sha256", h)
	}
	got, err := s.Get(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("roundtrip mismatch")
	}
}

func TestPutIsIdempotent(t *testing.T) {
	s := New(t.TempDir())
	ctx := context.Background()
	h1, err := s.Put(ctx, []byte("same content"))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := s.Put(ctx, []byte("same content"))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hashes differ: %s vs %s", h1, h2)
	}
}

func TestGetMissingIsErrNotFound(t *testing.T) {
	s := New(t.TempDir())
	_, err := s.Get(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestHas(t *testing.T) {
	s := New(t.TempDir())
	ctx := context.Background()
	h, err := s.Put(ctx, []byte("here"))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Has(ctx, h); err != nil || !ok {
		t.Fatalf("Has(present) = %v, %v", ok, err)
	}
	if ok, err := s.Has(ctx, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); err != nil || ok {
		t.Fatalf("Has(absent) = %v, %v", ok, err)
	}
}

func TestShortHashNoPanic(t *testing.T) {
	s := New(t.TempDir())
	ctx := context.Background()

	cases := []struct {
		name string
		hash string
	}{
		{"empty", ""},
		{"one-char", "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/Get", func(t *testing.T) {
			_, err := s.Get(ctx, domain.ContentHash(tc.hash))
			if !errors.Is(err, client.ErrNotFound) {
				t.Fatalf("Get(%q) err = %v, want ErrNotFound", tc.hash, err)
			}
		})
		t.Run(tc.name+"/Has", func(t *testing.T) {
			ok, err := s.Has(ctx, domain.ContentHash(tc.hash))
			if err != nil || ok {
				t.Fatalf("Has(%q) = %v, %v; want false, nil", tc.hash, ok, err)
			}
		})
	}
}

func TestShardedLayout(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	h, err := s.Put(context.Background(), []byte("sharded"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, string(h)[:2], string(h)[2:])
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected object at %s: %v", want, err)
	}
}
