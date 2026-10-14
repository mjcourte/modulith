package admin

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"gyoza/internal/engine"
	"gyoza/services/iam"
	"gyoza/services/storage"
)

// --- users ---

func (s *server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.q.users(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "users", "Users", users)
}

type userPage struct {
	User     UserRow
	Usage    OwnerUsage
	Projects []ProjectRow
	Shared   []ShareRow
	Invites  []InviteRow
	Assets   []AssetRow
}

func (s *server) showUser(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	var p userPage
	var err error
	if p.User, err = s.q.user(ctx, id); err != nil {
		s.fail(w, err)
		return
	}
	if p.Usage, err = s.q.usage(ctx, "user", id); err == nil {
		if p.Projects, err = s.q.projects(ctx, "user", id, ""); err == nil {
			if p.Shared, err = s.q.shares(ctx, "", id); err == nil {
				if p.Invites, err = s.q.invites(ctx, "", p.User.Email); err == nil {
					p.Assets, err = s.q.assets(ctx, "user", id, "", "", true)
				}
			}
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "user", p.User.Name, p)
}

// createUser does what the gateway's sign-in hook will: SignIn with a
// (here made-up) identity, which upserts the user.
func (s *server) createUser(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" {
		id = "dev-" + rand.Text()[:8]
	}
	res, err := s.iam.SignIn(r.Context(), iam.Identity{
		UserID:        id,
		Email:         strings.TrimSpace(r.FormValue("email")),
		EmailVerified: true,
		Name:          strings.TrimSpace(r.FormValue("name")),
	})
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	done(w, r, "/users/"+res.User.ID, nil, fmt.Sprintf("signed in %s (created: %v, pending invites: %d)", res.User.ID, res.Created, len(res.Invites)))
}

// deleteUser deletes the account as the user themselves, the only one who may.
func (s *server) deleteUser(w http.ResponseWriter, r *http.Request) {
	err := s.iam.DeleteAccount(r.Context(), r.PathValue("id"))
	done(w, r, "/users", err, "account deleted")
}

// --- orgs ---

func (s *server) listOrgs(w http.ResponseWriter, r *http.Request) {
	orgs, err := s.q.orgs(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "orgs", "Orgs", orgs)
}

type orgPage struct {
	Org      OrgRow
	Usage    OwnerUsage
	Members  []MemberRow
	Invites  []InviteRow
	Projects []ProjectRow
	Assets   []AssetRow
}

func (s *server) showOrg(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	var p orgPage
	var err error
	if p.Org, err = s.q.org(ctx, id); err != nil {
		s.fail(w, err)
		return
	}
	if p.Usage, err = s.q.usage(ctx, "org", id); err == nil {
		if p.Members, err = s.q.members(ctx, id); err == nil {
			if p.Invites, err = s.q.invites(ctx, id, ""); err == nil {
				if p.Projects, err = s.q.projects(ctx, "org", id, ""); err == nil {
					p.Assets, err = s.q.assets(ctx, "org", id, "", "", true)
				}
			}
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "org", p.Org.Name, p)
}

func (s *server) createOrg(w http.ResponseWriter, r *http.Request) {
	a, err := actor(r)
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	org, _, err := s.iam.CreateOrg(r.Context(), a, r.FormValue("name"))
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	done(w, r, "/orgs/"+org.ID, nil, "org created")
}

// withActor runs f as the acting user and redirects back with its outcome.
func withActor(w http.ResponseWriter, r *http.Request, ok string, f func(actor string) error) {
	a, err := actor(r)
	if err == nil {
		err = f(a)
	}
	done(w, r, "", err, ok)
}

func (s *server) renameOrg(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "renamed", func(a string) error {
		_, err := s.iam.RenameOrg(r.Context(), a, r.PathValue("id"), r.FormValue("name"))
		return err
	})
}

// deleteOrg always returns to the list: a delete that failed partway may
// already have removed the org's page.
func (s *server) deleteOrg(w http.ResponseWriter, r *http.Request) {
	a, err := actor(r)
	if err == nil {
		err = s.iam.DeleteOrg(r.Context(), a, r.PathValue("id"))
	}
	done(w, r, "/orgs", err, "org deleted")
}

func (s *server) inviteMember(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "invited", func(a string) error {
		_, err := s.iam.InviteMember(r.Context(), a, r.PathValue("id"), r.FormValue("email"), iam.Role(r.FormValue("role")))
		return err
	})
}

func (s *server) setRole(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "role changed", func(a string) error {
		_, err := s.iam.SetRole(r.Context(), a, r.PathValue("id"), r.PathValue("user"), iam.Role(r.FormValue("role")))
		return err
	})
}

// removeMember returns to the list when the last member left, which
// deletes the org.
func (s *server) removeMember(w http.ResponseWriter, r *http.Request) {
	a, err := actor(r)
	remaining := -1
	if err == nil {
		remaining, err = s.iam.RemoveMember(r.Context(), a, r.PathValue("id"), r.PathValue("user"))
	}
	if err == nil && remaining == 0 {
		done(w, r, "/orgs", nil, "last member removed; org deleted")
		return
	}
	done(w, r, "", err, "member removed")
}

// inviteAction accepts, declines (as the invitee) or revokes (as an admin).
func (s *server) inviteAction(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), r.PathValue("action")
	withActor(w, r, "invite "+action+"ed", func(a string) error {
		switch action {
		case "accept":
			_, err := s.iam.AcceptInvite(r.Context(), a, id)
			return err
		case "decline":
			return s.iam.DeclineInvite(r.Context(), a, id)
		case "revoke":
			return s.iam.RevokeInvite(r.Context(), a, id)
		}
		return fmt.Errorf("unknown invite action %q", action)
	})
}

// --- projects ---

func (s *server) listProjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	projects, err := s.q.projects(r.Context(), q.Get("owner_kind"), q.Get("owner_id"), q.Get("q"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "projects", "Projects", projects)
}

type projectPage struct {
	Project ProjectRow
	Shares  []ShareRow
	Pending []PendingShareRow
	Uses    []UseRow
	Assets  []AssetRow
	Clones  []ProjectRow
	Usable  []AssetRow // assets it may use in place, for "use asset"

	Snapshots []SnapshotRow // stored versions, newest first, to revert to
	// The live document as the acting user sees it (QueryDocument), or why not.
	Document    string
	DocumentErr string
}

func (s *server) showProject(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	var p projectPage
	var err error
	if p.Project, err = s.q.project(ctx, id); err != nil {
		s.fail(w, err)
		return
	}
	if p.Shares, err = s.q.shares(ctx, id, ""); err == nil {
		p.Pending, err = s.q.pendingShares(ctx, id)
	}
	if err == nil {
		if p.Uses, err = s.q.uses(ctx, id, ""); err == nil {
			if p.Assets, err = s.q.assets(ctx, "project", id, "", "", true); err == nil {
				if p.Usable, err = s.q.usableAssets(ctx, id); err == nil {
					p.Snapshots, err = s.q.snapshots(ctx, id)
				}
			}
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	p.Document, p.DocumentErr = s.currentDocument(r, id)
	s.render(w, r, "project", p.Project.Name, p)
}

// currentDocument is the project's live document as the acting user may read
// it, pretty-printed, or why it can't be shown. Loading it creates it if the
// project has none yet, as opening it in the editor would.
func (s *server) currentDocument(r *http.Request, projectID string) (string, string) {
	a, err := actor(r)
	if err != nil {
		return "", "pick an acting user to see the document"
	}
	v, err := s.docs.QueryDocument(r.Context(), a, projectID, engine.Query{Format: engine.QueryNative})
	if err != nil {
		return "", err.Error()
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, v.Data, "", "  "); err != nil {
		return string(v.Data), ""
	}
	return pretty.String(), ""
}

// revertDocument puts the project's document back to a stored version, as
// the acting user (it needs write access).
func (s *server) revertDocument(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "document reverted", func(a string) error {
		v, err := strconv.Atoi(r.FormValue("version"))
		if err != nil {
			return err
		}
		_, err = s.docs.RevertDocument(r.Context(), a, r.PathValue("id"), v)
		return err
	})
}

func (s *server) createProject(w http.ResponseWriter, r *http.Request) {
	a, err := actor(r)
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	p, err := s.iam.CreateProject(r.Context(), a, owner(r), r.FormValue("name"), r.FormValue("org_locked") == "on")
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	done(w, r, "/projects/"+p.ID, nil, "project created")
}

// createTemplate is a service-to-service call: no actor.
func (s *server) createTemplate(w http.ResponseWriter, r *http.Request) {
	p, err := s.iam.CreateTemplate(r.Context(), r.FormValue("name"))
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	done(w, r, "/projects/"+p.ID, nil, "template created")
}

func (s *server) renameProject(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "renamed", func(a string) error {
		_, err := s.iam.RenameProject(r.Context(), a, r.PathValue("id"), r.FormValue("name"))
		return err
	})
}

func (s *server) lockProject(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "org lock changed", func(a string) error {
		_, err := s.iam.SetOrgLocked(r.Context(), a, r.PathValue("id"), r.FormValue("locked") == "true")
		return err
	})
}

func (s *server) moveProject(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "moved to org", func(a string) error {
		_, err := s.iam.MoveProjectToOrg(r.Context(), a, r.PathValue("id"))
		return err
	})
}

func (s *server) shareProject(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "shared", func(a string) error {
		_, err := s.iam.ShareProject(r.Context(), a, r.PathValue("id"), r.FormValue("email"), iam.Access(r.FormValue("access")))
		return err
	})
}

func (s *server) unshareProject(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "share removed", func(a string) error {
		return s.iam.Unshare(r.Context(), a, r.PathValue("id"), r.PathValue("user"))
	})
}

func (s *server) revokePendingShare(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "pending share revoked", func(a string) error {
		return s.iam.RevokePendingShare(r.Context(), a, r.PathValue("id"), r.FormValue("email"))
	})
}

func (s *server) cloneProject(w http.ResponseWriter, r *http.Request) {
	a, err := actor(r)
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	p, err := s.iam.CloneProject(r.Context(), a, r.PathValue("id"), owner(r))
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	done(w, r, "/projects/"+p.ID, nil, "cloned")
}

func (s *server) deleteProject(w http.ResponseWriter, r *http.Request) {
	a, err := actor(r)
	if err == nil {
		if r.FormValue("template") == "true" {
			err = s.iam.DeleteTemplate(r.Context(), r.PathValue("id"))
		} else {
			err = s.iam.DeleteProject(r.Context(), a, r.PathValue("id"))
		}
	}
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	done(w, r, "/projects", nil, "project deleted")
}

// deleteOwned is storage's GDPR cascade for one owner: a service-to-service
// call, so no actor.
func (s *server) deleteOwned(w http.ResponseWriter, r *http.Request) {
	o := storage.Owner{Kind: storage.OwnerKind(r.FormValue("owner_kind")), ID: r.FormValue("owner_id")}
	rep, err := s.storage.DeleteOwned(r.Context(), o)
	done(w, r, "", err, fmt.Sprintf("%+v", rep))
}
