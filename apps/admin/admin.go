// Package admin is a development-only web UI over the database and the
// services. It reads iam.* and storage.* with SQL (exempt from the
// one-schema rule, being a dev tool) and makes every change through
// iam.Service and storage.Service, acting as whichever user is picked in the
// header. It trusts its caller completely: never expose it beyond localhost.
package admin

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gyoza/services/iam"
	"gyoza/services/storage"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

const actorCookie = "gyoza_admin_actor"

type server struct {
	q       *queries
	iam     iam.Service
	storage storage.Service
	pages   map[string]*template.Template
	http    *http.Client // PUTs uploads to presigned URLs
}

// New returns the admin UI. iamSvc and storageSvc are normally gRPC clients
// to a running gyoza; pool reads both schemas.
func New(pool *pgxpool.Pool, iamSvc iam.Service, storageSvc storage.Service) (http.Handler, error) {
	s := &server{
		q:       &queries{pool: pool},
		iam:     iamSvc,
		storage: storageSvc,
		http:    &http.Client{Timeout: 10 * time.Minute},
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	return s.routes(), nil
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("POST /act", s.act)

	mux.HandleFunc("GET /users", s.listUsers)
	mux.HandleFunc("GET /users/{id}", s.showUser)
	mux.HandleFunc("POST /users", s.createUser)
	mux.HandleFunc("POST /users/{id}/delete", s.deleteUser)

	mux.HandleFunc("GET /orgs", s.listOrgs)
	mux.HandleFunc("GET /orgs/{id}", s.showOrg)
	mux.HandleFunc("POST /orgs", s.createOrg)
	mux.HandleFunc("POST /orgs/{id}/rename", s.renameOrg)
	mux.HandleFunc("POST /orgs/{id}/delete", s.deleteOrg)
	mux.HandleFunc("POST /orgs/{id}/invites", s.inviteMember)
	mux.HandleFunc("POST /orgs/{id}/members/{user}/role", s.setRole)
	mux.HandleFunc("POST /orgs/{id}/members/{user}/remove", s.removeMember)
	mux.HandleFunc("POST /invites/{id}/{action}", s.inviteAction)

	mux.HandleFunc("GET /projects", s.listProjects)
	mux.HandleFunc("GET /projects/{id}", s.showProject)
	mux.HandleFunc("POST /projects", s.createProject)
	mux.HandleFunc("POST /templates", s.createTemplate)
	mux.HandleFunc("POST /projects/{id}/rename", s.renameProject)
	mux.HandleFunc("POST /projects/{id}/lock", s.lockProject)
	mux.HandleFunc("POST /projects/{id}/move-to-org", s.moveProject)
	mux.HandleFunc("POST /projects/{id}/share", s.shareProject)
	mux.HandleFunc("POST /projects/{id}/shares/{user}/remove", s.unshareProject)
	mux.HandleFunc("POST /projects/{id}/clone", s.cloneProject)
	mux.HandleFunc("POST /projects/{id}/delete", s.deleteProject)

	mux.HandleFunc("GET /assets", s.listAssets)
	mux.HandleFunc("GET /assets/{id}", s.showAsset)
	mux.HandleFunc("GET /assets/{id}/download", s.downloadAsset)
	mux.HandleFunc("POST /assets/upload", s.uploadAsset)
	mux.HandleFunc("POST /assets/{id}/delete", s.deleteAsset)
	mux.HandleFunc("POST /assets/{id}/restore", s.restoreAsset)
	mux.HandleFunc("POST /assets/{id}/revert", s.revertAsset)
	mux.HandleFunc("GET /folders", s.listFolders)
	mux.HandleFunc("GET /objects", s.listObjects)
	mux.HandleFunc("POST /owners/delete-owned", s.deleteOwned)
	mux.HandleFunc("GET /uploads", s.listUploads)
	mux.HandleFunc("POST /uses", s.recordUse)
	mux.HandleFunc("POST /uses/forget", s.forgetUse)
	return mux
}

// --- rendering ---

var funcs = template.FuncMap{
	"ownerURL": ownerURL,
	"list":     func(s ...string) []string { return s },
	"time": func(t any) string {
		switch v := t.(type) {
		case time.Time:
			return v.Local().Format("2006-01-02 15:04:05")
		case *time.Time:
			if v == nil {
				return ""
			}
			return v.Local().Format("2006-01-02 15:04:05")
		}
		return fmt.Sprint(t)
	},
	"deref": func(p any) any {
		switch v := p.(type) {
		case *string:
			if v != nil {
				return *v
			}
		case *int32:
			if v != nil {
				return *v
			}
		case *int64:
			if v != nil {
				return *v
			}
		}
		return ""
	},
	"bytes": func(n int64) string {
		const unit = 1024
		if n < unit {
			return fmt.Sprintf("%d B", n)
		}
		div, exp := int64(unit), 0
		for m := n / unit; m >= unit; m /= unit {
			div *= unit
			exp++
		}
		return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
	},
	"indent": func(depth int32) string { return strings.Repeat("    ", int(depth)) },
}

// ownerURL links an owner to its page ("" for gyoza).
func ownerURL(kind, id string) string {
	switch kind {
	case "user":
		return "/users/" + url.PathEscape(id)
	case "org":
		return "/orgs/" + url.PathEscape(id)
	case "project":
		return "/projects/" + url.PathEscape(id)
	}
	return ""
}

// parseTemplates builds one template set per page: layout.html plus the page,
// which defines "content".
func (s *server) parseTemplates() error {
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	s.pages = map[string]*template.Template{}
	for _, name := range names {
		page := strings.TrimSuffix(strings.TrimPrefix(name, "templates/"), ".html")
		if page == "layout" {
			continue
		}
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", name)
		if err != nil {
			return err
		}
		s.pages[page] = t
	}
	return nil
}

// view is what every page gets: the layout's needs plus the page's Data.
type view struct {
	Title   string
	Actor   string
	Actors  []UserRow
	Flash   string
	IsError bool
	Path    string
	Query   url.Values
	Data    any
}

func (s *server) render(w http.ResponseWriter, r *http.Request, page, title string, data any) {
	actors, err := s.q.users(r.Context(), "")
	if err != nil {
		s.fail(w, err)
		return
	}
	v := view{
		Title:   title,
		Actor:   actorOf(r),
		Actors:  actors,
		Flash:   r.URL.Query().Get("flash"),
		IsError: r.URL.Query().Get("err") == "1",
		Path:    r.URL.Path,
		Query:   r.URL.Query(),
		Data:    data,
	}
	t, ok := s.pages[page]
	if !ok {
		s.fail(w, fmt.Errorf("no template %q", page))
		return
	}
	// htmx requests (the live search boxes) want only the content block.
	name := "layout.html"
	if r.Header.Get("HX-Request") == "true" {
		name = "content"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, name, v); err != nil {
		slog.Error("admin: render", "page", page, "err", err)
	}
}

// fail reports an unexpected error (a broken query, a missing template).
func (s *server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.Error("admin", "err", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// done redirects after a POST (post/redirect/get) to back, or to the page the
// form was on, carrying err's message or ok as a flash.
func done(w http.ResponseWriter, r *http.Request, back string, err error, ok string) {
	if back == "" {
		back = r.FormValue("back")
	}
	if back == "" {
		back = r.Referer()
	}
	u, perr := url.Parse(back)
	if perr != nil || back == "" {
		u = &url.URL{Path: "/"}
	}
	q := u.Query()
	q.Del("flash")
	q.Del("err")
	if err != nil {
		q.Set("flash", err.Error())
		q.Set("err", "1")
	} else if ok != "" {
		q.Set("flash", ok)
	}
	u.RawQuery = q.Encode()
	// Only redirect within this site, whatever the Referer says.
	http.Redirect(w, r, u.RequestURI(), http.StatusSeeOther)
}

// --- the acting user ---

var errNoActor = errors.New("pick a user to act as (top right) first")

func actorOf(r *http.Request) string {
	c, err := r.Cookie(actorCookie)
	if err != nil {
		return ""
	}
	v, _ := url.QueryUnescape(c.Value)
	return v
}

// actor returns the acting user, or errNoActor.
func actor(r *http.Request) (string, error) {
	if a := actorOf(r); a != "" {
		return a, nil
	}
	return "", errNoActor
}

func (s *server) act(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     actorCookie,
		Value:    url.QueryEscape(r.FormValue("actor")),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	done(w, r, "", nil, "")
}

// owner reads an owner from the form fields owner_kind and owner_id. A user
// owner with no ID means the acting user.
func owner(r *http.Request) storage.Owner {
	o := storage.Owner{Kind: storage.OwnerKind(r.FormValue("owner_kind")), ID: strings.TrimSpace(r.FormValue("owner_id"))}
	if o.Kind == storage.OwnerUser && o.ID == "" {
		o.ID = actorOf(r)
	}
	return o
}

func (s *server) dashboard(w http.ResponseWriter, r *http.Request) {
	counts, err := s.q.counts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	orphans, err := s.q.orphans(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "dashboard", "Dashboard", struct {
		Counts  []Count
		Orphans []OrphanOwner
	}{counts, orphans})
}
