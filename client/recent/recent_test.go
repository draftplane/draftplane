package recent_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/draftplane/draftplane/client/recent"
	"github.com/draftplane/draftplane/domain"
)

func TestTouchKeepsTheNewestFirstAndOneEntryPerThing(t *testing.T) {
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	file := recent.Entry{Path: "/home/m/notes.md", OpenedAt: at}
	plan := recent.Entry{PlanID: "l_a", OpenedAt: at.Add(time.Minute)}
	fileNowAPlan := recent.Entry{PlanID: "l_n", Path: "/home/m/notes.md", OpenedAt: at.Add(2 * time.Minute)}
	for _, tc := range []struct {
		name    string
		touches []recent.Entry
		want    []recent.Entry
	}{
		{"the first open", []recent.Entry{file}, []recent.Entry{file}},
		{"newest first", []recent.Entry{file, plan}, []recent.Entry{plan, file}},
		{"reopening moves it to the top", []recent.Entry{file, plan, file}, []recent.Entry{file, plan}},
		{"a file that became a plan is one entry", []recent.Entry{file, plan, fileNowAPlan}, []recent.Entry{fileNowAPlan, plan}},
		{"an entry naming nothing is not recorded", []recent.Entry{{OpenedAt: at}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "recent.json")
			for _, e := range tc.touches {
				if err := recent.Touch(path, e); err != nil {
					t.Fatal(err)
				}
			}
			got, err := recent.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.EqualFunc(got, tc.want, func(a, b recent.Entry) bool {
				return a.PlanID == b.PlanID && a.Path == b.Path && a.OpenedAt.Equal(b.OpenedAt)
			}) {
				t.Fatalf("entries = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestTouchKeepsKeep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent.json")
	for i := range recent.Keep + 3 {
		if err := recent.Touch(path, recent.Entry{PlanID: domain.PlanID(fmt.Sprintf("l_%02d", i))}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := recent.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != recent.Keep || got[0].PlanID != domain.PlanID(fmt.Sprintf("l_%02d", recent.Keep+2)) {
		t.Fatalf("kept %d entries, newest %q; want %d, newest the last touched", len(got), got[0].PlanID, recent.Keep)
	}
}

func TestForgetDropsEveryEntryNamingIt(t *testing.T) {
	file := recent.Entry{Path: "/p/notes.md"}
	plan := recent.Entry{PlanID: "l_a", Path: "/p/a.md"}
	other := recent.Entry{PlanID: "p_b"}
	for _, tc := range []struct {
		name   string
		forget recent.Entry
		want   []recent.Entry
	}{
		{"a file by its path", recent.Entry{Path: "/p/notes.md"}, []recent.Entry{other, plan}},
		{"a plan by its id", recent.Entry{PlanID: "l_a"}, []recent.Entry{other, file}},
		{"a plan by its id and its file", recent.Entry{PlanID: "p_x", Path: "/p/a.md"}, []recent.Entry{other, file}},
		{"something never opened", recent.Entry{PlanID: "l_zz"}, []recent.Entry{other, plan, file}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "recent.json")
			for _, e := range []recent.Entry{file, plan, other} {
				if err := recent.Touch(path, e); err != nil {
					t.Fatal(err)
				}
			}
			if err := recent.Forget(path, tc.forget); err != nil {
				t.Fatal(err)
			}
			got, _ := recent.Load(path)
			if !slices.EqualFunc(got, tc.want, func(a, b recent.Entry) bool { return a.PlanID == b.PlanID && a.Path == b.Path }) {
				t.Fatalf("entries = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestTheFileIsPrivateAndForgetNeverCreatesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "recent.json")
	if err := recent.Forget(path, recent.Entry{Path: "/p/x.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Forget on no file created one (stat err %v)", err)
	}
	if err := recent.Touch(path, recent.Entry{Path: "/p/x.md"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("recent.json mode = %o, want 0600", perm)
	}
}

func TestAnUndecodableFileIsRefusedOnReadAndStartedOverOnWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recent.json")
	if err := os.WriteFile(path, []byte(`{"not": "a list"`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := recent.Load(path)
	if err == nil || strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("Load = %v, want a refusal naming the file and not the decoder's own words", err)
	}
	if err := recent.Touch(path, recent.Entry{Path: "/p/x.md"}); err != nil {
		t.Fatalf("Touch over an undecodable file: %v, want it started over", err)
	}
	if got, err := recent.Load(path); err != nil || len(got) != 1 {
		t.Fatalf("after Touch: %+v, %v; want the one new entry", got, err)
	}
}
