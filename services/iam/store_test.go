package iam_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"gyoza/services/iam"
)

// runStoreTests checks the behaviour every Store implementation must share.
// newStore must return an empty store for each call.
func runStoreTests(t *testing.T, newStore func(t *testing.T) iam.Store) {
	tests := []struct {
		name string
		fn   func(*testing.T, iam.Store)
	}{
		// Users: store_users_test.go
		{"UpsertUserCreatesThenUpdates", testUpsertUserCreatesThenUpdates},
		{"UpsertUserNormalises", testUpsertUserNormalises},
		{"UpsertUserRejectsInvalid", testUpsertUserRejectsInvalid},
		{"EmailsUnique", testEmailsUnique},
		{"GetUserMissing", testGetUserMissing},

		// Orgs and members: store_orgs_test.go
		{"CreateOrg", testCreateOrg},
		{"CreateOrgRejects", testCreateOrgRejects},
		{"RenameOrg", testRenameOrg},
		{"ListMembersOrder", testListMembersOrder},
		{"SetRole", testSetRole},
		{"RemoveMember", testRemoveMember},

		// Invites: store_invites_test.go
		{"CreateInvite", testCreateInvite},
		{"CreateInviteRejects", testCreateInviteRejects},
		{"ReinviteReplaces", testReinviteReplaces},
		{"ListInvites", testListInvites},
		{"AcceptInvite", testAcceptInvite},
		{"AcceptInviteRejects", testAcceptInviteRejects},
		{"DeleteInvite", testDeleteInvite},

		// Projects and shares: store_projects_test.go
		{"CreateProject", testCreateProject},
		{"CreateProjectRejects", testCreateProjectRejects},
		{"ListMyProjects", testListMyProjects},
		{"ListProjectsByOwner", testListProjectsByOwner},
		{"ListProjectsPaging", testListProjectsPaging},
		{"RenameProject", testRenameProject},
		{"TransferProject", testTransferProject},
		{"ProjectStateAndLock", testProjectStateAndLock},
		{"ShareProject", testShareProject},
		{"ShareLockedProject", testShareLockedProject},
		{"Unshare", testUnshare},

		// Deletions: store_deletions_test.go
		{"MarkProjectDeleting", testMarkProjectDeleting},
		{"DeleteProject", testDeleteProject},
		{"MarkOrgDeleting", testMarkOrgDeleting},
		{"AcceptInviteToDeletingOrg", testAcceptInviteToDeletingOrg},
		{"DeleteOrg", testDeleteOrg},
		{"MarkUserDeleting", testMarkUserDeleting},
		{"TransferUserProjects", testTransferUserProjects},
		{"ForgetUser", testForgetUser},
		{"DeleteUser", testDeleteUser},
		{"ListDeleting", testListDeleting},
		{"DeleteExpiredInvites", testDeleteExpiredInvites},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.fn(t, newStore(t))
		})
	}
}

// letTimePass makes the next timestamp differ from the last: stores keep
// microseconds, so two quick calls can share one.
func letTimePass() { time.Sleep(2 * time.Millisecond) }

// isUUIDv7 reports whether id is a canonical UUIDv7.
func isUUIDv7(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.Version() == 7 && u.String() == id
}

// mustUser signs a user in with ID name and email name@example.com.
func mustUser(t *testing.T, s iam.Store, name string) iam.User {
	t.Helper()
	u, _, err := s.UpsertUser(t.Context(), iam.Identity{UserID: name, Email: name + "@example.com", EmailVerified: true, Name: name})
	if err != nil {
		t.Fatalf("UpsertUser(%s): %v", name, err)
	}
	return u
}

// mustOrg creates an org with creatorID as its admin.
func mustOrg(t *testing.T, s iam.Store, name, creatorID string) iam.Org {
	t.Helper()
	o, _, err := s.CreateOrg(t.Context(), iam.Org{Name: name}, creatorID)
	if err != nil {
		t.Fatalf("CreateOrg(%s): %v", name, err)
	}
	return o
}

// mustInvite invites email to orgID with role, expiring after ttl (negative:
// already expired).
func mustInvite(t *testing.T, s iam.Store, orgID, email string, role iam.Role, ttl time.Duration) iam.Invite {
	t.Helper()
	inv, err := s.CreateInvite(t.Context(), iam.Invite{OrgID: orgID, Email: email, Role: role, InvitedBy: "admin", ExpiresAt: time.Now().Add(ttl)})
	if err != nil {
		t.Fatalf("CreateInvite(%s): %v", email, err)
	}
	return inv
}

// mustJoin adds an existing user to orgID with role, the way it really
// happens: an invite, then accepting it.
func mustJoin(t *testing.T, s iam.Store, orgID string, u iam.User, role iam.Role) iam.Member {
	t.Helper()
	inv := mustInvite(t, s, orgID, u.Email, role, time.Hour)
	m, err := s.AcceptInvite(t.Context(), inv.ID, u.ID)
	if err != nil {
		t.Fatalf("AcceptInvite(%s): %v", u.ID, err)
	}
	return m
}

// memberIDs lists the user IDs of members, in order.
func memberIDs(ms []iam.Member) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.UserID
	}
	return out
}
