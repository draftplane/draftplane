package app

import (
	"testing"

	"github.com/draftplane/draftplane/keymap"
)

// TestHintLineNamesExactlyItsOwnRowKindsSegments pins browse's bar on the two
// "Your plans" row kinds, whole: a plan row names what acts on a plan, and the
// +N more row says enter EXPANDS rather than opens. A Recently opened file row's
// own bar is pinned beside its section (TestSortIsNotOfferedInRecentlyOpened).
func TestHintLineNamesExactlyItsOwnRowKindsSegments(t *testing.T) {
	m := NewList(newListFixture(t).svc, keymap.Default(), nil)
	m.width, m.height = 80, 24
	m.applyRefresh(listSectionItems("l_", 30))
	for _, tc := range []struct {
		name string
		kind rowKind
		want string
	}{
		{"a plan", rowPlan, "enter open · / filter · s sort · e rename · d delete · i info · ? keys · q quit"},
		{"the +N more row", rowMore, "enter expand · / filter · s sort · e rename · d delete · i info · ? keys · q quit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.cursor = -1
			for i, r := range m.rows {
				if r.kind == tc.kind {
					m.cursor = i
					break
				}
			}
			if m.cursor < 0 {
				t.Fatalf("test setup: no row of kind %d", tc.kind)
			}
			if got := m.hintLine(); got != tc.want {
				t.Fatalf("hintLine() = %q, want %q", got, tc.want)
			}
		})
	}
}
