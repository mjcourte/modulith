package directory

// Internal tests of the project and share rules.

import (
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mjcourte/modulith/services/storage"
)

var (
	ruleUser  = storage.Owner{Kind: storage.OwnerUser, ID: "ann"}
	ruleOrg   = storage.Owner{Kind: storage.OwnerOrg, ID: "acme"}
	ruleModulith = storage.Owner{Kind: storage.OwnerModulith}
)

func TestProjectNormalize(t *testing.T) {
	p, err := Project{Owner: ruleUser, Name: "  Poster  ", CreatedBy: "ann"}.normalize()
	if err != nil || p.Name != "Poster" || p.State != ProjectReady {
		t.Fatalf("normalize = %+v, %v; want trimmed name, state ready", p, err)
	}
	for _, owner := range []storage.Owner{ruleOrg, ruleModulith} {
		if _, err := (Project{Owner: owner, Name: "X"}).normalize(); err != nil {
			t.Errorf("owner %v: %v", owner, err)
		}
	}
	clone := Project{Owner: ruleUser, Name: "X", State: ProjectCloning, ClonedFrom: newID()}
	if _, err := clone.normalize(); err != nil {
		t.Errorf("a clone with its source: %v", err)
	}

	bad := map[string]Project{
		"no owner":         {Name: "X"},
		"project owner":    {Owner: storage.Owner{Kind: storage.OwnerProject, ID: "p1"}, Name: "X"},
		"modulith with ID":    {Owner: storage.Owner{Kind: storage.OwnerModulith, ID: "x"}, Name: "X"},
		"no name":          {Owner: ruleUser, Name: "  "},
		"long name":        {Owner: ruleUser, Name: strings.Repeat("a", MaxNameLen+1)},
		"unknown state":    {Owner: ruleUser, Name: "X", State: "archived"},
		"clone, no source": {Owner: ruleUser, Name: "X", State: ProjectCloning},
		"bad source ID":    {Owner: ruleUser, Name: "X", ClonedFrom: "p1"},
		"bad creator":      {Owner: ruleUser, Name: "X", CreatedBy: "has space"},
	}
	for name, p := range bad {
		if _, err := p.normalize(); !errors.Is(err, ErrInvalidProject) {
			t.Errorf("%s: %v, want ErrInvalidProject", name, err)
		}
	}
}

func TestCheckTransfer(t *testing.T) {
	userProject := Project{Owner: ruleUser}
	if err := checkTransfer(userProject, ruleOrg); err != nil {
		t.Errorf("user -> org: %v", err)
	}
	bad := map[string]struct {
		p  Project
		to storage.Owner
	}{
		"to modulith":      {userProject, ruleModulith},
		"from modulith":    {Project{Owner: ruleModulith}, ruleOrg},
		"to a project":  {userProject, storage.Owner{Kind: storage.OwnerProject, ID: "p1"}},
		"to no one":     {userProject, storage.Owner{}},
		"malformed org": {userProject, storage.Owner{Kind: storage.OwnerOrg}},
	}
	for name, tt := range bad {
		if err := checkTransfer(tt.p, tt.to); !errors.Is(err, ErrInvalidProject) {
			t.Errorf("%s: %v, want ErrInvalidProject", name, err)
		}
	}
}

func TestShareNormalize(t *testing.T) {
	valid := Share{ProjectID: newID(), UserID: "bob", Access: AccessEditor, SharedBy: "ann"}
	if _, err := valid.normalize(); err != nil {
		t.Fatalf("valid share: %v", err)
	}
	for name, change := range map[string]func(*Share){
		"no access":     func(s *Share) { s.Access = AccessNone },
		"unknown level": func(s *Share) { s.Access = "owner" },
		"no user":       func(s *Share) { s.UserID = "" },
		"bad user":      func(s *Share) { s.UserID = "has space" },
		"no sharer":     func(s *Share) { s.SharedBy = "" },
	} {
		s := valid
		change(&s)
		if _, err := s.normalize(); !errors.Is(err, ErrInvalidShare) {
			t.Errorf("%s: %v, want ErrInvalidShare", name, err)
		}
	}
}

func TestCheckShareable(t *testing.T) {
	for _, owner := range []storage.Owner{ruleUser, ruleOrg} {
		if err := checkShareable(Project{Owner: owner}); err != nil {
			t.Errorf("%v-owned project: %v", owner.Kind, err)
		}
	}
	if err := checkShareable(Project{Owner: ruleModulith}); !errors.Is(err, ErrInvalidShare) {
		t.Errorf("modulith template = %v, want ErrInvalidShare", err)
	}
}

func TestProjectQueryNormalize(t *testing.T) {
	q, err := ProjectQuery{VisibleTo: "ann"}.normalize()
	if err != nil || q.PageSize != DefaultPageSize {
		t.Errorf("default page size: %+v, %v", q, err)
	}
	if q, _ := (ProjectQuery{Owner: ruleModulith, PageSize: MaxPageSize + 1}).normalize(); q.PageSize != MaxPageSize {
		t.Errorf("page size not capped: %d", q.PageSize)
	}
	bad := map[string]ProjectQuery{
		"no scope":        {},
		"two scopes":      {VisibleTo: "ann", Owner: ruleOrg},
		"bad user":        {VisibleTo: "has space"},
		"project owner":   {Owner: storage.Owner{Kind: storage.OwnerProject, ID: "p1"}},
		"malformed owner": {Owner: storage.Owner{Kind: storage.OwnerUser}},
		"negative size":   {VisibleTo: "ann", PageSize: -1},
	}
	for name, q := range bad {
		if _, err := q.normalize(); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("%s: %v, want ErrInvalidQuery", name, err)
		}
	}
}

func TestProjectPaging(t *testing.T) {
	var ps []Project
	for range 5 {
		ps = append(ps, Project{ID: newID()})
	}

	page := projectPageOf(slices.Clone(ps[:3]), 3)
	if len(page.Projects) != 3 || page.NextPageToken != "" {
		t.Errorf("exactly a page: %d projects, token %q; want 3, none", len(page.Projects), page.NextPageToken)
	}
	page = projectPageOf(slices.Clone(ps[:4]), 3) // one extra: there's more
	if len(page.Projects) != 3 || page.NextPageToken == "" {
		t.Fatalf("a page and one more: %d projects, token %q", len(page.Projects), page.NextPageToken)
	}

	after, err := ProjectQuery{PageToken: page.NextPageToken}.afterID()
	if err != nil || after != ps[2].ID {
		t.Errorf("token decodes to %q, %v; want the page's last ID %s", after, err, ps[2].ID)
	}
	if after, err := (ProjectQuery{}).afterID(); err != nil || after != "" {
		t.Errorf("no token = %q, %v; want the start", after, err)
	}
	for _, token := range []string{"!!!", base64.RawURLEncoding.EncodeToString([]byte("not-a-uuid"))} {
		if _, err := (ProjectQuery{PageToken: token}).afterID(); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("token %q = %v, want ErrInvalidQuery", token, err)
		}
	}
}

func TestOrgLockRules(t *testing.T) {
	if _, err := (Project{Owner: ruleOrg, Name: "X", OrgLocked: true}).normalize(); err != nil {
		t.Errorf("locked org project: %v", err)
	}
	for _, owner := range []storage.Owner{ruleUser, ruleModulith} {
		if _, err := (Project{Owner: owner, Name: "X", OrgLocked: true}).normalize(); !errors.Is(err, ErrInvalidProject) {
			t.Errorf("locked %v project = %v, want ErrInvalidProject", owner.Kind, err)
		}
		if err := checkOrgLock(Project{Owner: owner}); !errors.Is(err, ErrInvalidProject) {
			t.Errorf("checkOrgLock(%v) = %v, want ErrInvalidProject", owner.Kind, err)
		}
	}
	if err := checkOrgLock(Project{Owner: ruleOrg}); err != nil {
		t.Errorf("checkOrgLock(org) = %v", err)
	}
}

func TestShareActive(t *testing.T) {
	open := Project{Owner: ruleOrg}
	locked := Project{Owner: ruleOrg, OrgLocked: true}
	tests := []struct {
		name    string
		p       Project
		userOrg string
		want    bool
	}{
		{"unlocked, outsider", open, "", true},
		{"unlocked, other org", open, "other", true},
		{"locked, member", locked, "acme", true},
		{"locked, outsider", locked, "", false},
		{"locked, other org", locked, "other", false},
		// A locked project that somehow isn't an org's: nobody's share counts.
		{"locked user project", Project{Owner: ruleUser, OrgLocked: true}, "ann", false},
	}
	for _, tt := range tests {
		if got := shareActive(tt.p, tt.userOrg); got != tt.want {
			t.Errorf("%s: shareActive = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestSortShares(t *testing.T) {
	t0 := now()
	shares := []Share{
		{UserID: "cat", CreatedAt: t0.Add(time.Second)},
		{UserID: "bob", CreatedAt: t0},
		{UserID: "ann", CreatedAt: t0}, // tied with bob, after him in the input
	}
	sortShares(shares)
	var got []string
	for _, s := range shares {
		got = append(got, s.UserID)
	}
	if want := []string{"ann", "bob", "cat"}; !slices.Equal(got, want) {
		t.Errorf("sortShares = %v, want %v", got, want)
	}
}
