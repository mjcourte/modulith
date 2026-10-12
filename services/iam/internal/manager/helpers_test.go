package manager_test

// Store fixtures for the Manager and StorageAuthorizer tests: they seed the
// directory directly, below the Manager's permission checks.

import (
	"testing"
	"time"

	"gyoza/services/iam/internal/directory"
	"gyoza/services/storage"
)

func userOwner(id string) storage.Owner { return storage.Owner{Kind: storage.OwnerUser, ID: id} }
func orgOwner(id string) storage.Owner  { return storage.Owner{Kind: storage.OwnerOrg, ID: id} }

var gyoza = storage.Owner{Kind: storage.OwnerGyoza}

// mustUser signs a user in with ID name and email name@example.com.
func mustUser(t *testing.T, s directory.Store, name string) directory.User {
	t.Helper()
	u, _, err := s.UpsertUser(t.Context(), directory.Identity{UserID: name, Email: name + "@example.com", EmailVerified: true, Name: name})
	if err != nil {
		t.Fatalf("UpsertUser(%s): %v", name, err)
	}
	return u
}

// mustOrg creates an org with creatorID as its admin.
func mustOrg(t *testing.T, s directory.Store, name, creatorID string) directory.Org {
	t.Helper()
	o, _, err := s.CreateOrg(t.Context(), directory.Org{Name: name}, creatorID)
	if err != nil {
		t.Fatalf("CreateOrg(%s): %v", name, err)
	}
	return o
}

// mustJoin adds an existing user to orgID with role: an invite, then accepting it.
func mustJoin(t *testing.T, s directory.Store, orgID string, u directory.User, role directory.Role) directory.Member {
	t.Helper()
	inv, err := s.CreateInvite(t.Context(), directory.Invite{OrgID: orgID, Email: u.Email, Role: role, InvitedBy: "admin", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("CreateInvite(%s): %v", u.Email, err)
	}
	m, err := s.AcceptInvite(t.Context(), inv.ID, u.ID)
	if err != nil {
		t.Fatalf("AcceptInvite(%s): %v", u.ID, err)
	}
	return m
}

func mustProject(t *testing.T, s directory.Store, p directory.Project) directory.Project {
	t.Helper()
	created, err := s.CreateProject(t.Context(), p)
	if err != nil {
		t.Fatalf("CreateProject(%s): %v", p.Name, err)
	}
	return created
}

func mustShare(t *testing.T, s directory.Store, projectID, userID string, access directory.Access) directory.Share {
	t.Helper()
	sh, err := s.ShareProject(t.Context(), directory.Share{ProjectID: projectID, UserID: userID, Access: access, SharedBy: "someone"})
	if err != nil {
		t.Fatalf("ShareProject(%s): %v", userID, err)
	}
	return sh
}
