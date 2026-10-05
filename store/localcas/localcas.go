// Package localcas is the local content-addressed snapshot store:
// <root>/<hh>/<rest-of-hash>, one file per reviewed version. client/localfs
// keeps every plan's content in it.
package localcas

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/xdg"
)

type Store struct {
	root string
}

func New(root string) *Store {
	return &Store{root: root}
}

// dataHome resolves <data-home> -- $XDG_DATA_HOME/draftplane when set and
// non-empty, else ~/.local/share/draftplane. cmd/draftplane's own dataPath
// implements the identical algorithm a second time for the state file (no
// shared package both could import without a cycle), and
// TestDataPathObjectsSiblingMatchesLocalCASDefaultRoot pins that they agree.
func dataHome() (string, error) {
	dir, err := xdg.DataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "draftplane"), nil
}

// DefaultRoot is the XDG data location for the store: <data-home>/objects.
func DefaultRoot() (string, error) {
	home, err := dataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "objects"), nil
}

func (s *Store) objectPath(h domain.ContentHash) string {
	return filepath.Join(s.root, string(h)[:2], string(h)[2:])
}

func (s *Store) Put(_ context.Context, content []byte) (domain.ContentHash, error) {
	h := domain.HashContent(content)
	dest := s.objectPath(h)
	if _, err := os.Stat(dest); err == nil {
		return h, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), "put-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return h, nil
}

func (s *Store) Get(_ context.Context, h domain.ContentHash) ([]byte, error) {
	if len(h) < 2 {
		return nil, fmt.Errorf("snapshot %s: %w", h, client.ErrNotFound)
	}
	content, err := os.ReadFile(s.objectPath(h))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("snapshot %s: %w", h.Short(), client.ErrNotFound)
	}
	return content, err
}

func (s *Store) Has(_ context.Context, h domain.ContentHash) (bool, error) {
	if len(h) < 2 {
		return false, nil
	}
	_, err := os.Stat(s.objectPath(h))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
