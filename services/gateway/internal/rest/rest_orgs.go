package rest

import (
	"net/http"
	"time"

	"github.com/mjcourte/modulith/services/iam"
)

// ---- Wire types

type User struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	EmailVerified bool      `json:"emailVerified"`
	Name          string    `json:"name"`
	AvatarURL     string    `json:"avatarUrl"`
	CreatedAt     time.Time `json:"createdAt"`
}

func userOut(u iam.User) User {
	return User{ID: u.ID, Email: u.Email, EmailVerified: u.EmailVerified, Name: u.Name,
		AvatarURL: u.ProviderAvatarURL, CreatedAt: u.CreatedAt.UTC()}
}

type Org struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func orgOut(o iam.Org) Org { return Org{ID: o.ID, Name: o.Name, CreatedAt: o.CreatedAt.UTC()} }

type Member struct {
	OrgID    string    `json:"orgId"`
	UserID   string    `json:"userId"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joinedAt"`
}

func memberOut(m iam.Member) Member {
	return Member{OrgID: m.OrgID, UserID: m.UserID, Role: string(m.Role), JoinedAt: m.JoinedAt.UTC()}
}

type Invite struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"orgId"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	InvitedBy string    `json:"invitedBy"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func inviteOut(i iam.Invite) Invite {
	return Invite{ID: i.ID, OrgID: i.OrgID, Email: i.Email, Role: string(i.Role), InvitedBy: i.InvitedBy,
		CreatedAt: i.CreatedAt.UTC(), ExpiresAt: i.ExpiresAt.UTC()}
}

func mapSlice[T, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in)) // never null in JSON
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

// remaining is what removing someone from an org returns.
type remaining struct {
	RemainingMembers int `json:"remainingMembers"`
}

// ---- Me

func (s *Server) routesMe(mux *http.ServeMux) {
	// The signed-in user: profile, org and role, and pending invites.
	mux.Handle("GET /me", serve(func(r *http.Request, actor string) (any, error) {
		me, err := s.iam.GetMe(r.Context(), actor)
		if err != nil {
			return nil, err
		}
		invites, err := s.iam.ListMyInvites(r.Context(), actor)
		if err != nil {
			return nil, err
		}
		out := struct {
			User       User     `json:"user"`
			Org        *Org     `json:"org"`        // null if in no org
			Membership *Member  `json:"membership"` // null if in no org
			Invites    []Invite `json:"invites"`
		}{User: userOut(me.User), Invites: mapSlice(invites, inviteOut)}
		if me.Org != nil && me.Membership != nil {
			o, m := orgOut(*me.Org), memberOut(*me.Membership)
			out.Org, out.Membership = &o, &m
		}
		return out, nil
	}))
	mux.Handle("DELETE /me", serve(func(r *http.Request, actor string) (any, error) {
		return nil, s.iam.DeleteAccount(r.Context(), actor)
	}))
	mux.Handle("GET /me/invites", serve(func(r *http.Request, actor string) (any, error) {
		invites, err := s.iam.ListMyInvites(r.Context(), actor)
		return mapSlice(invites, inviteOut), err
	}))
	// Leave the org the user belongs to.
	mux.Handle("DELETE /me/org", serve(func(r *http.Request, actor string) (any, error) {
		n, err := s.iam.LeaveOrg(r.Context(), actor)
		return remaining{n}, err
	}))
}

// ---- Orgs, members and invites

func (s *Server) routesOrgs(mux *http.ServeMux) {
	type nameBody struct {
		Name string `json:"name"`
	}
	mux.Handle("POST /orgs", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[nameBody](r)
		if err != nil {
			return nil, err
		}
		o, m, err := s.iam.CreateOrg(r.Context(), actor, b.Name)
		if err != nil {
			return nil, err
		}
		return struct {
			Org        Org    `json:"org"`
			Membership Member `json:"membership"`
		}{orgOut(o), memberOut(m)}, nil
	}))
	mux.Handle("GET /orgs/{id}", serve(func(r *http.Request, actor string) (any, error) {
		o, err := s.iam.GetOrg(r.Context(), actor, r.PathValue("id"))
		return orgOut(o), err
	}))
	mux.Handle("PATCH /orgs/{id}", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[nameBody](r)
		if err != nil {
			return nil, err
		}
		o, err := s.iam.RenameOrg(r.Context(), actor, r.PathValue("id"), b.Name)
		return orgOut(o), err
	}))
	mux.Handle("DELETE /orgs/{id}", serve(func(r *http.Request, actor string) (any, error) {
		return nil, s.iam.DeleteOrg(r.Context(), actor, r.PathValue("id"))
	}))

	mux.Handle("GET /orgs/{id}/members", serve(func(r *http.Request, actor string) (any, error) {
		ms, err := s.iam.ListMembers(r.Context(), actor, r.PathValue("id"))
		return mapSlice(ms, memberOut), err
	}))
	mux.Handle("PATCH /orgs/{id}/members/{userId}", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Role string `json:"role"`
		}](r)
		if err != nil {
			return nil, err
		}
		m, err := s.iam.SetRole(r.Context(), actor, r.PathValue("id"), r.PathValue("userId"), iam.Role(b.Role))
		return memberOut(m), err
	}))
	mux.Handle("DELETE /orgs/{id}/members/{userId}", serve(func(r *http.Request, actor string) (any, error) {
		n, err := s.iam.RemoveMember(r.Context(), actor, r.PathValue("id"), r.PathValue("userId"))
		return remaining{n}, err
	}))

	mux.Handle("POST /orgs/{id}/invites", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}](r)
		if err != nil {
			return nil, err
		}
		inv, err := s.iam.InviteMember(r.Context(), actor, r.PathValue("id"), b.Email, iam.Role(b.Role))
		return inviteOut(inv), err
	}))
	mux.Handle("GET /orgs/{id}/invites", serve(func(r *http.Request, actor string) (any, error) {
		invites, err := s.iam.ListOrgInvites(r.Context(), actor, r.PathValue("id"))
		return mapSlice(invites, inviteOut), err
	}))
	mux.Handle("DELETE /invites/{id}", serve(func(r *http.Request, actor string) (any, error) {
		return nil, s.iam.RevokeInvite(r.Context(), actor, r.PathValue("id"))
	}))
	mux.Handle("POST /invites/{id}/accept", serve(func(r *http.Request, actor string) (any, error) {
		m, err := s.iam.AcceptInvite(r.Context(), actor, r.PathValue("id"))
		return memberOut(m), err
	}))
	mux.Handle("POST /invites/{id}/decline", serve(func(r *http.Request, actor string) (any, error) {
		return nil, s.iam.DeclineInvite(r.Context(), actor, r.PathValue("id"))
	}))
}
