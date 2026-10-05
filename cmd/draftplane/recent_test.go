package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/draftplane/draftplane/client/recent"
)

func TestReviewRemembersWhatItOpened(t *testing.T) {
	local := newLocalFixture(t)
	ctx := attrCtx("alice")
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(file, []byte("# Scratch notes\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		raw     string
		wantErr bool
		want    recent.Entry
	}{
		{"a file", file, false, recent.Entry{Path: file}},
		{"a typo is refused and remembered nowhere", filepath.Join(dir, "nope.md"), true, recent.Entry{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "recent.json")
			_, err := openAndRemember(ctx, local, tc.raw, path, time.Now())
			if (err != nil) != tc.wantErr {
				t.Fatalf("openAndRemember(%q) err = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
			got, _ := recent.Load(path)
			if tc.wantErr {
				if len(got) != 0 {
					t.Fatalf("a refused open recorded %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Path != tc.want.Path || got[0].PlanID != tc.want.PlanID {
				t.Fatalf("recorded %+v, want %+v", got, tc.want)
			}
		})
	}
}
