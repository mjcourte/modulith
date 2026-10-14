package directory_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"gyoza/services/iam/internal/directory"
)

// mustSharePending shares projectID with email, expiring after ttl
// (negative: already expired).
func mustSharePending(t *testing.T, s directory.Store, projectID, email string, access directory.Access, ttl time.Duration) directory.Share {
	t.Helper()
	sh, err := s.SharePending(t.Context(), directory.Share{ProjectID: projectID, Email: email, Access: access, SharedBy: "someone", ExpiresAt: time.Now().Add(ttl)})
	if err != nil {
		t.Fatalf("SharePending(%s): %v", email, err)
	}
	return sh
}

// pendingEmails lists the emails of pending shares, in order.
func pendingEmails(shares []directory.Share) []string {
	var out []string
	for _, sh := range shares {
		out = append(out, sh.Email)
	}
	return out
}

func testSharePending(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann := mustUser(t, s, "ann")
	p := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "Poster"})

	first := mustSharePending(t, s, p.ID, " Dan@Example.com", directory.AccessViewer, time.Hour)
	if first.Email != "dan@example.com" || first.UserID != "" || !first.Pending() || first.CreatedAt.IsZero() || first.ExpiresAt.IsZero() {
		t.Errorf("new pending share = %+v; want dan@example.com, no user", first)
	}
	letTimePass()
	// Sharing again refreshes it: one pending share, its creation time kept.
	again := mustSharePending(t, s, p.ID, "dan@example.com", directory.AccessEditor, 2*time.Hour)
	if again.Access != directory.AccessEditor || !again.CreatedAt.Equal(first.CreatedAt) ||
		!again.UpdatedAt.After(first.UpdatedAt) || !again.ExpiresAt.After(first.ExpiresAt) {
		t.Errorf("shared again = %+v; want editor, same CreatedAt, later UpdatedAt and ExpiresAt", again)
	}
	letTimePass()
	mustSharePending(t, s, p.ID, "cat@example.com", directory.AccessViewer, -time.Hour) // expired: still listed
	other := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "Other"})
	mustSharePending(t, s, other.ID, "eve@example.com", directory.AccessViewer, time.Hour)
	if got, err := s.ListPendingShares(ctx, p.ID); err != nil || !slices.Equal(pendingEmails(got), []string{"dan@example.com", "cat@example.com"}) {
		t.Errorf("ListPendingShares = %v, %v; want dan then cat (oldest first, expired included)", pendingEmails(got), err)
	}
	// Pending shares grant nothing and aren't shares.
	if shares, _ := s.ListShares(ctx, p.ID); len(shares) != 0 {
		t.Errorf("ListShares includes pending shares: %+v", shares)
	}

	acme := mustOrg(t, s, "Acme", ann.ID)
	locked := mustProject(t, s, directory.Project{Owner: orgOwner(acme.ID), Name: "Brand", OrgLocked: true})
	template := mustProject(t, s, directory.Project{Owner: gyoza, Name: "T"})
	valid := directory.Share{ProjectID: p.ID, Email: "x@example.com", Access: directory.AccessViewer, SharedBy: "x", ExpiresAt: time.Now().Add(time.Hour)}
	with := func(f func(*directory.Share)) directory.Share { sh := valid; f(&sh); return sh }
	tests := []struct {
		name string
		sh   directory.Share
		want error
	}{
		{"a template", with(func(sh *directory.Share) { sh.ProjectID = template.ID }), directory.ErrInvalidShare},
		{"a locked project", with(func(sh *directory.Share) { sh.ProjectID = locked.ID }), directory.ErrInvalidShare},
		{"missing project", with(func(sh *directory.Share) { sh.ProjectID = uuid.NewString() }), directory.ErrProjectNotFound},
		{"not an email", with(func(sh *directory.Share) { sh.Email = "Dan <dan@example.com>" }), directory.ErrInvalidShare},
		{"a user too", with(func(sh *directory.Share) { sh.UserID = ann.ID }), directory.ErrInvalidShare},
		{"no level", with(func(sh *directory.Share) { sh.Access = "" }), directory.ErrInvalidShare},
		{"no sharer", with(func(sh *directory.Share) { sh.SharedBy = "" }), directory.ErrInvalidShare},
		{"no expiry", with(func(sh *directory.Share) { sh.ExpiresAt = time.Time{} }), directory.ErrInvalidShare},
	}
	for _, tt := range tests {
		if _, err := s.SharePending(ctx, tt.sh); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
	if _, err := s.ListPendingShares(ctx, uuid.NewString()); !errors.Is(err, directory.ErrProjectNotFound) {
		t.Errorf("ListPendingShares(missing) = %v, want ErrProjectNotFound", err)
	}

	if err := s.DeletePendingShare(ctx, p.ID, "DAN@example.com"); err != nil {
		t.Fatalf("DeletePendingShare: %v", err)
	}
	if got, _ := s.ListPendingShares(ctx, p.ID); !slices.Equal(pendingEmails(got), []string{"cat@example.com"}) {
		t.Errorf("after deleting dan's: %v", pendingEmails(got))
	}
	for _, args := range [][2]string{{p.ID, "dan@example.com"}, {uuid.NewString(), "dan@example.com"}, {"junk", "junk"}} {
		if err := s.DeletePendingShare(ctx, args[0], args[1]); err != nil {
			t.Errorf("DeletePendingShare(%v) again = %v, want nil (safe to retry)", args, err)
		}
	}
}

// Claiming turns the address's live pending shares into shares, never
// lowering access the user already has, and leaves what can't apply yet.
func testClaimPendingShares(t *testing.T, s directory.Store) {
	ctx := t.Context()
	ann := mustUser(t, s, "ann")
	acme := mustOrg(t, s, "Acme", ann.ID)
	fresh := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "fresh"})
	upgrade := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "upgrade"})
	keep := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "keep"})
	expired := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "expired"})
	brand := mustProject(t, s, directory.Project{Owner: orgOwner(acme.ID), Name: "brand"})
	doomed := mustProject(t, s, directory.Project{Owner: userOwner(ann.ID), Name: "doomed"})

	mustSharePending(t, s, fresh.ID, "dan@example.com", directory.AccessViewer, time.Hour)
	mustSharePending(t, s, upgrade.ID, "dan@example.com", directory.AccessEditor, time.Hour)
	mustSharePending(t, s, keep.ID, "dan@example.com", directory.AccessViewer, time.Hour)
	mustSharePending(t, s, expired.ID, "dan@example.com", directory.AccessViewer, -time.Hour)
	mustSharePending(t, s, brand.ID, "dan@example.com", directory.AccessViewer, time.Hour)
	mustSharePending(t, s, doomed.ID, "dan@example.com", directory.AccessViewer, time.Hour)
	mustSharePending(t, s, fresh.ID, "eve@example.com", directory.AccessViewer, time.Hour) // someone else's
	if _, err := s.SetOrgLocked(ctx, brand.ID, true); err != nil {
		t.Fatalf("SetOrgLocked: %v", err)
	}
	if _, err := s.MarkProjectDeleting(ctx, doomed.ID); err != nil {
		t.Fatalf("MarkProjectDeleting: %v", err)
	}

	dan := mustUser(t, s, "dan")
	mustShare(t, s, upgrade.ID, dan.ID, directory.AccessViewer)
	keptShare := mustShare(t, s, keep.ID, dan.ID, directory.AccessEditor)
	letTimePass()

	if _, err := s.ClaimPendingShares(ctx, "ghost", "dan@example.com", time.Now()); !errors.Is(err, directory.ErrUserNotFound) {
		t.Errorf("claim for an unknown user = %v, want ErrUserNotFound", err)
	}
	n, err := s.ClaimPendingShares(ctx, dan.ID, "DAN@example.com", time.Now())
	if err != nil || n != 3 {
		t.Fatalf("ClaimPendingShares = %d, %v; want 3 (fresh, upgrade, keep)", n, err)
	}
	for _, c := range []struct {
		project string
		want    directory.Access
	}{{fresh.ID, directory.AccessViewer}, {upgrade.ID, directory.AccessEditor}, {keep.ID, directory.AccessEditor}} {
		sh, err := s.GetShare(ctx, c.project, dan.ID)
		if err != nil || sh.Access != c.want || sh.SharedBy != "someone" && c.project != keep.ID {
			t.Errorf("dan's share of %s = %+v, %v; want %s from the pending share", c.project, sh, err, c.want)
		}
	}
	if sh, _ := s.GetShare(ctx, keep.ID, dan.ID); sh != keptShare {
		t.Errorf("a greater share changed: %+v, was %+v", sh, keptShare)
	}
	for _, p := range []string{fresh.ID, upgrade.ID, keep.ID} {
		if got, _ := s.ListPendingShares(ctx, p); slices.Contains(pendingEmails(got), "dan@example.com") {
			t.Errorf("project %s still has dan's pending share", p)
		}
	}
	// Not claimed: expired, locked to an org dan isn't in, being deleted.
	for _, p := range []string{expired.ID, brand.ID, doomed.ID} {
		if _, err := s.GetShare(ctx, p, dan.ID); !errors.Is(err, directory.ErrShareNotFound) {
			t.Errorf("project %s was shared with dan: %v", p, err)
		}
		if got, _ := s.ListPendingShares(ctx, p); !slices.Equal(pendingEmails(got), []string{"dan@example.com"}) {
			t.Errorf("project %s lost dan's pending share: %v", p, pendingEmails(got))
		}
	}
	if got, _ := s.ListPendingShares(ctx, fresh.ID); !slices.Equal(pendingEmails(got), []string{"eve@example.com"}) {
		t.Errorf("another address's pending share changed: %v", pendingEmails(got))
	}

	// Joining the org makes the locked project's share apply.
	mustJoin(t, s, acme.ID, dan, directory.RoleMember)
	if n, err := s.ClaimPendingShares(ctx, dan.ID, dan.Email, time.Now()); err != nil || n != 1 {
		t.Errorf("claim after joining the org = %d, %v; want 1", n, err)
	}
	if n, err := s.ClaimPendingShares(ctx, dan.ID, dan.Email, time.Now()); err != nil || n != 0 {
		t.Errorf("claim again = %d, %v; want 0", n, err)
	}
}

func testDeleteExpiredPendingShares(t *testing.T, s directory.Store) {
	ctx := t.Context()
	p := mustProject(t, s, directory.Project{Owner: userOwner(mustUser(t, s, "ann").ID), Name: "Poster"})
	mustSharePending(t, s, p.ID, "old@example.com", directory.AccessViewer, -2*time.Hour)
	mustSharePending(t, s, p.ID, "older@example.com", directory.AccessViewer, -3*time.Hour)
	mustSharePending(t, s, p.ID, "live@example.com", directory.AccessViewer, time.Hour)

	if n, err := s.DeleteExpiredPendingShares(ctx, time.Now(), 1); err != nil || n != 1 {
		t.Fatalf("DeleteExpiredPendingShares(limit 1) = %d, %v", n, err)
	}
	if got, _ := s.ListPendingShares(ctx, p.ID); slices.Contains(pendingEmails(got), "older@example.com") {
		t.Errorf("the longest-expired pending share should go first: %v", pendingEmails(got))
	}
	if n, err := s.DeleteExpiredPendingShares(ctx, time.Now(), 10); err != nil || n != 1 {
		t.Errorf("DeleteExpiredPendingShares = %d, %v; want the other expired one", n, err)
	}
	if got, _ := s.ListPendingShares(ctx, p.ID); !slices.Equal(pendingEmails(got), []string{"live@example.com"}) {
		t.Errorf("left %v, want only the live one", pendingEmails(got))
	}
	if _, err := s.DeleteExpiredPendingShares(ctx, time.Now(), 0); !errors.Is(err, directory.ErrInvalidQuery) {
		t.Errorf("limit 0 = %v, want ErrInvalidQuery", err)
	}
}
