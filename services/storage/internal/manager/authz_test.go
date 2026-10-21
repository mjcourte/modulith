package manager_test

import (
	"errors"
	"testing"

	sm "github.com/mjcourte/modulith/services/storage/internal/manager"
)

func TestAllowAll(t *testing.T) {
	ctx := t.Context()
	var a sm.AllowAll
	if err := a.CanWrite(ctx, "anyone", acme, "p1"); err != nil {
		t.Errorf("CanWrite = %v, want allowed", err)
	}
	// Even the stand-in refuses calls with no user: that's a wiring bug.
	if err := a.CanRead(ctx, "", acme, ""); !errors.Is(err, sm.ErrForbidden) {
		t.Errorf("CanRead with no actor = %v, want ErrForbidden", err)
	}
}

func TestRuleAuthorizer(t *testing.T) {
	r := sm.NewRuleAuthorizer()
	r.AllowOwner("member", acme, sm.AccessWrite)
	r.AllowOwner("viewer", acme, sm.AccessRead)
	r.AllowProject("guest", "p1", sm.AccessRead)
	r.AllowProject("editor", "p1", sm.AccessWrite)
	// A misconfigured rule for an empty user ID: calls without a user must
	// still be refused, whatever the rules say.
	r.AllowOwner("", acme, sm.AccessWrite)

	tests := []struct {
		name    string
		actor   string
		write   bool // CanWrite, otherwise CanRead
		owner   string
		project string
		allowed bool
	}{
		{"member writes org files", "member", true, "acme", "", true},
		{"viewer reads org files", "viewer", false, "acme", "", true},
		{"viewer can't write", "viewer", true, "acme", "", false},
		{"write implies read", "member", false, "acme", "", true},
		{"guest reads via shared project", "guest", false, "acme", "p1", true},
		{"guest can't write the shared project", "guest", true, "acme", "p1", false},
		{"guest can't read outside the project", "guest", false, "acme", "", false},
		{"guest can't read another project", "guest", false, "acme", "p2", false},
		// As in iam: a project grants write only to files it owns.
		{"project editor writes its files", "editor", true, "p1", "p1", true},
		{"project editor reads associated files", "editor", false, "acme", "p1", true},
		{"project editor can't write associated files", "editor", true, "acme", "p1", false},
		{"stranger can't read", "stranger", false, "acme", "", false},
		{"user writes own library", "alice", true, "alice", "", true},
		{"user can't read someone else's", "alice", false, "bob", "", false},
		{"no actor", "", false, "acme", "", false},
		{"no actor, even with a rule for it", "", true, "acme", "", false},
	}
	owners := map[string]struct{ kind, id string }{
		"acme": {"org", "acme"}, "p1": {"project", "p1"}, "alice": {"user", "alice"}, "bob": {"user", "bob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := owners[tt.owner]
			owner := acme
			if o.kind == "user" {
				owner = alice
				owner.ID = o.id
			}
			if o.kind == "project" {
				owner = project
			}
			check := r.CanRead
			if tt.write {
				check = r.CanWrite
			}
			err := check(t.Context(), tt.actor, owner, tt.project)
			if tt.allowed && err != nil {
				t.Errorf("got %v, want allowed", err)
			}
			if !tt.allowed && !errors.Is(err, sm.ErrForbidden) {
				t.Errorf("got %v, want ErrForbidden", err)
			}
		})
	}
}

func TestRuleAuthorizerConcurrentUse(t *testing.T) {
	r := sm.NewRuleAuthorizer()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			r.AllowOwner("member", acme, sm.AccessWrite)
		}
	}()
	for range 1000 {
		_ = r.CanRead(t.Context(), "member", acme, "")
	}
	<-done
}
