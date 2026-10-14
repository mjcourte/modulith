package rest

import (
	"net/http"
	"time"

	"gyoza/services/iam"
)

// ---- Wire types

type Project struct {
	ID         string    `json:"id"`
	Owner      Owner     `json:"owner"`
	Name       string    `json:"name"`
	State      string    `json:"state"`
	ClonedFrom string    `json:"clonedFrom,omitempty"`
	OrgLocked  bool      `json:"orgLocked"`
	CreatedBy  string    `json:"createdBy"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func projectOut(p iam.Project) Project {
	return Project{ID: p.ID, Owner: ownerOut(p.Owner), Name: p.Name, State: string(p.State), ClonedFrom: p.ClonedFrom,
		OrgLocked: p.OrgLocked, CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC()}
}

type ProjectPage struct {
	Projects      []Project `json:"projects"`
	NextPageToken string    `json:"nextPageToken"`
}

func projectPageOut(p iam.ProjectPage) ProjectPage {
	return ProjectPage{Projects: mapSlice(p.Projects, projectOut), NextPageToken: p.NextPageToken}
}

// Share is a user's share, or a pending one (email and expiresAt set, userId
// ""): to an address with no account yet, claimed when someone signs up
// with it.
type Share struct {
	ProjectID string     `json:"projectId"`
	UserID    string     `json:"userId"`
	Email     string     `json:"email,omitempty"`
	Access    string     `json:"access"`
	SharedBy  string     `json:"sharedBy"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

func shareOut(sh iam.Share) Share {
	out := Share{ProjectID: sh.ProjectID, UserID: sh.UserID, Email: sh.Email, Access: string(sh.Access), SharedBy: sh.SharedBy,
		CreatedAt: sh.CreatedAt.UTC(), UpdatedAt: sh.UpdatedAt.UTC()}
	if sh.Pending() {
		out.ExpiresAt = timeOut(&sh.ExpiresAt)
	}
	return out
}

// ---- Routes

func (s *Server) routesProjects(mux *http.ServeMux) {
	// list reads ?pageSize=&pageToken= and calls one of iam's list calls.
	list := func(fn func(r *http.Request, actor string, size int, token string) (iam.ProjectPage, error)) http.HandlerFunc {
		return serve(func(r *http.Request, actor string) (any, error) {
			size, err := queryInt(r, "pageSize")
			if err != nil {
				return nil, err
			}
			page, err := fn(r, actor, size, r.URL.Query().Get("pageToken"))
			return projectPageOut(page), err
		})
	}

	mux.Handle("POST /projects", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Owner     Owner  `json:"owner"`
			Name      string `json:"name"`
			OrgLocked bool   `json:"orgLocked"`
		}](r)
		if err != nil {
			return nil, err
		}
		owner, err := b.Owner.in()
		if err != nil {
			return nil, err
		}
		p, err := s.iam.CreateProject(r.Context(), actor, owner, b.Name, b.OrgLocked)
		return projectOut(p), err
	}))
	mux.Handle("GET /projects", list(func(r *http.Request, actor string, size int, token string) (iam.ProjectPage, error) {
		return s.iam.ListMyProjects(r.Context(), actor, size, token)
	}))
	mux.Handle("GET /templates", list(func(r *http.Request, actor string, size int, token string) (iam.ProjectPage, error) {
		return s.iam.ListTemplates(r.Context(), actor, size, token)
	}))
	// A project with the actor's access to it.
	mux.Handle("GET /projects/{id}", serve(func(r *http.Request, actor string) (any, error) {
		v, err := s.iam.GetProject(r.Context(), actor, r.PathValue("id"))
		return struct {
			Project Project `json:"project"`
			Access  string  `json:"access"`
		}{projectOut(v.Project), string(v.Access)}, err
	}))
	mux.Handle("PATCH /projects/{id}", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Name string `json:"name"`
		}](r)
		if err != nil {
			return nil, err
		}
		p, err := s.iam.RenameProject(r.Context(), actor, r.PathValue("id"), b.Name)
		return projectOut(p), err
	}))
	mux.Handle("DELETE /projects/{id}", serve(func(r *http.Request, actor string) (any, error) {
		return nil, s.iam.DeleteProject(r.Context(), actor, r.PathValue("id"))
	}))
	// Clone into target (the actor's library or their org). If copying fails
	// part-way the clone stays hidden and iam's cleanup finishes it; the
	// error is returned.
	mux.Handle("POST /projects/{id}/clone", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Target Owner `json:"target"`
		}](r)
		if err != nil {
			return nil, err
		}
		target, err := b.Target.in()
		if err != nil {
			return nil, err
		}
		p, err := s.iam.CloneProject(r.Context(), actor, r.PathValue("id"), target)
		if err != nil {
			return nil, err
		}
		return projectOut(p), nil
	}))
	mux.Handle("POST /projects/{id}/move-to-org", serve(func(r *http.Request, actor string) (any, error) {
		p, err := s.iam.MoveProjectToOrg(r.Context(), actor, r.PathValue("id"))
		return projectOut(p), err
	}))
	mux.Handle("PUT /projects/{id}/org-lock", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Locked bool `json:"locked"`
		}](r)
		if err != nil {
			return nil, err
		}
		p, err := s.iam.SetOrgLocked(r.Context(), actor, r.PathValue("id"), b.Locked)
		return projectOut(p), err
	}))

	mux.Handle("GET /projects/{id}/shares", serve(func(r *http.Request, actor string) (any, error) {
		shares, err := s.iam.ListShares(r.Context(), actor, r.PathValue("id"))
		return mapSlice(shares, shareOut), err
	}))
	// Share by email, or change the access. An address with no account gets a
	// pending share: the same 200, so nobody learns who has an account.
	mux.Handle("POST /projects/{id}/shares", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Email  string `json:"email"`
			Access string `json:"access"`
		}](r)
		if err != nil {
			return nil, err
		}
		sh, err := s.iam.ShareProject(r.Context(), actor, r.PathValue("id"), b.Email, iam.Access(b.Access))
		return shareOut(sh), err
	}))
	mux.Handle("DELETE /projects/{id}/shares/{userId}", serve(func(r *http.Request, actor string) (any, error) {
		return nil, s.iam.Unshare(r.Context(), actor, r.PathValue("id"), r.PathValue("userId"))
	}))
	mux.Handle("DELETE /projects/{id}/pending-shares/{email}", serve(func(r *http.Request, actor string) (any, error) {
		return nil, s.iam.RevokePendingShare(r.Context(), actor, r.PathValue("id"), r.PathValue("email"))
	}))
}
