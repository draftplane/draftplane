package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/draftplane/draftplane/client"
	"github.com/draftplane/draftplane/domain"
	"github.com/draftplane/draftplane/session"
)

// emptySvc is a minimal client.PlanService for exercising
// openReviewTarget's disambiguation without a store: it holds no plan, so
// ResolvePlan answers client.ErrNoPlan and PlanByID answers client.ErrNotFound
// for every id a test can name. Every other method is left to the nil embedded
// client.PlanService and must not be called by these tests --
// openReviewTarget's job is choosing between session.Open and
// session.OpenByID, not exercising what either does beyond that choice.
type emptySvc struct {
	client.PlanService
}

func (f *emptySvc) ResolvePlan(context.Context, string) (domain.Plan, error) {
	return domain.Plan{}, client.ErrNoPlan
}

func (f *emptySvc) PlanByID(_ context.Context, id domain.PlanID) (domain.Plan, error) {
	return domain.Plan{}, fmt.Errorf("plan %s: %w", id, client.ErrNotFound)
}

// TestOpenReviewTargetPrefersAReadableFile proves the disambiguation rule's first
// arm: an argument naming a real file on disk is opened as a file, never treated
// as a plan id, even though nothing about its string shape rules that out.
func TestOpenReviewTargetPrefersAReadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fake := &emptySvc{}

	s, err := openReviewTarget(context.Background(), fake, path)
	if err != nil {
		t.Fatalf("openReviewTarget: %v", err)
	}
	if s.Path != path {
		t.Errorf("Path = %q, want %q", s.Path, path)
	}
}

// TestOpenReviewTargetResolvesAGoneFileAgainstTheIndex proves one step at
// this door. `draftplane review <a path whose file is gone>` asks this
// machine's index first and opens what it finds through session.OpenPlan, the
// same function the list's door calls, so the two reach the identical session
// and the identical fault: the file is gone, and the session says so. A
// mistyped path is covered separately by
// TestOpenReviewTargetFoldsTheFileFailureIntoAnUnknownIDError below -- the
// index misses, and today's error stands.
func TestOpenReviewTargetResolvesAGoneFileAgainstTheIndex(t *testing.T) {
	local := newLocalFixture(t)
	ctx := attrCtx("alice")
	path := filepath.Join(t.TempDir(), "rollout.md")
	if err := os.WriteFile(path, []byte("# Rollout\n\nthe working copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := session.Open(ctx, local, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "Rollout"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	got, err := openReviewTarget(ctx, local, path)
	if err != nil {
		t.Fatalf("openReviewTarget(%s) = %v, want the plan this machine's index files that path under", path, err)
	}
	if !got.FromSnapshot() {
		t.Error("FromSnapshot() = false, want true -- there is no file left to follow")
	}
	if got.SourceFault == nil {
		t.Fatalf("SourceFault = nil, want state %s", session.SourceFileGone)
	}
	if got.SourceFault.State != session.SourceFileGone {
		t.Errorf("SourceFault.State = %s, want %s", got.SourceFault.State, session.SourceFileGone)
	}
	if got.SourceFault.Path != path {
		t.Errorf("SourceFault.Path = %q, want %q", got.SourceFault.Path, path)
	}
}

// resolveFailingSvc answers ResolvePlan with a failure that is NOT
// client.ErrNoPlan -- an unreadable state.json, say -- standing in for an index
// that could not be ASKED, as distinct from one that answered "nothing here".
// Every other call falls through to the embedded fake.
type resolveFailingSvc struct {
	client.PlanService
	err error
}

func (s resolveFailingSvc) ResolvePlan(context.Context, string) (domain.Plan, error) {
	return domain.Plan{}, s.err
}

// TestOpenReviewTargetDoesNotBlameTheUserForAnIndexItCouldNotRead is the other
// half of that step. A MISS there is a real answer -- nobody has ever filed a
// plan under this path, so the argument was probably a typo and today's two-part
// error is right. A FAILURE is not that answer: treating the two alike would
// report the user's spelling as the problem while this machine's own bookkeeping
// was what failed, and would make a plan they really do have look like one they
// never created.
func TestOpenReviewTargetDoesNotBlameTheUserForAnIndexItCouldNotRead(t *testing.T) {
	broken := errors.New("the index is unreadable")
	svc := resolveFailingSvc{PlanService: &emptySvc{}, err: broken}

	_, err := openReviewTarget(context.Background(), svc, "some-plan.md")
	if !errors.Is(err, broken) {
		t.Fatalf("err = %v, want errors.Is(err, broken) -- the index failure is the thing that went wrong", err)
	}
	if strings.Contains(err.Error(), "if you meant a local file") {
		t.Errorf("err = %q, reads as a mistyped path -- nothing here established that the path is wrong", err.Error())
	}
}

// TestLocalReviewFileDistinguishesFilesFromEverythingElse enumerates
// localReviewFile's own cases rather than trusting the one openReviewTarget
// tests above happen to exercise.
func TestLocalReviewFileDistinguishesFilesFromEverythingElse(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(file, []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"a real file is a file", file, false},
		{"a directory is not a file", subdir, true},
		{"an l_ id is not a file", "l_abc123", true},
		{"a nonexistent relative path is not a file", "does-not-exist.md", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := localReviewFile(tc.path)
			if (err != nil) != tc.wantErr {
				t.Errorf("localReviewFile(%q) err = %v, want non-nil: %v", tc.path, err, tc.wantErr)
			}
		})
	}
}

// TestLocalReviewFileDirectoryErrorNamesThePath proves the directory case's
// synthesized error is diagnosable on its own -- os.Stat has no complaint about a
// directory (it exists and is readable), so unlike every other non-file case here
// this one has no underlying error to report unless localReviewFile builds one
// itself.
func TestLocalReviewFileDirectoryErrorNamesThePath(t *testing.T) {
	dir := t.TempDir()
	_, err := localReviewFile(dir)
	if err == nil {
		t.Fatal("localReviewFile(directory): want an error, got nil")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("localReviewFile(%q) err = %v, want it to name the path", dir, err)
	}
}

// TestOpenReviewTargetFoldsTheFileFailureIntoAnUnknownIDError: a mistyped path
// must not be diagnosed as an unknown plan id alone, with no mention that it
// might just be a typo. Both interpretations' reasons must appear in the one
// error openReviewTarget returns, because this argument is raw keyboard input --
// unlike mcptools' source, which normally arrives machine-supplied from a prior
// tool call -- so a fat-fingered path is the realistic case, not an edge one.
func TestOpenReviewTargetFoldsTheFileFailureIntoAnUnknownIDError(t *testing.T) {
	fake := &emptySvc{} // it holds no plan, so PlanByID never matches "typo-plan.md"

	_, err := openReviewTarget(context.Background(), fake, "typo-plan.md")
	if err == nil {
		t.Fatal("openReviewTarget: want an error for a path that is neither a file nor a known id")
	}
	if !errors.Is(err, client.ErrNotFound) {
		t.Errorf("err = %v, want it to still wrap OpenVersion's own not-found (errors.Is client.ErrNotFound)", err)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, reads as a missing FILE rather than an unknown plan id", err)
	}
	if !strings.Contains(err.Error(), "typo-plan.md") {
		t.Errorf("err = %q, want it to name the path so a human can tell this might be a typo", err.Error())
	}
	// The discriminating assertion. statErr is folded in via %v, not %w, so it
	// never joins the errors.Is chain -- this literal phrase, which only
	// openReviewTarget's own fold ever adds, is the sole proof it ran at all.
	// Without it this test is vacuous: id := domain.PlanID(rawPath) means
	// OpenVersion's own not-found error already names "typo-plan.md" on its own, so
	// both assertions above would pass exactly the same way whether or not statErr
	// was ever folded in.
	if !strings.Contains(err.Error(), "if you meant a local file") {
		t.Errorf("err = %q, want it to also name the file-open failure (statErr), not just the id "+
			"interpretation -- both reasons must appear, since this argument is raw keyboard input", err.Error())
	}
}
