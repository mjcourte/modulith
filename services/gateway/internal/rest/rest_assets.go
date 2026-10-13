package rest

import (
	"net/http"
	"time"

	"gyoza/services/storage"
)

// ---- Wire types

type Asset struct {
	ID             string     `json:"id"`
	Owner          Owner      `json:"owner"`
	ProjectID      string     `json:"projectId,omitempty"`
	FolderID       string     `json:"folderId"` // "" is the top level
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	Tags           []string   `json:"tags"`
	ContentType    string     `json:"contentType"`
	Category       string     `json:"category"`
	CurrentVersion int        `json:"currentVersion"`
	CreatedBy      string     `json:"createdBy"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	DeletedAt      *time.Time `json:"deletedAt"` // set while in the trash
}

func assetOut(a storage.Asset) Asset {
	tags := a.Tags
	if tags == nil {
		tags = []string{}
	}
	return Asset{ID: a.ID, Owner: ownerOut(a.Owner), ProjectID: a.ProjectID, FolderID: a.FolderID, Name: a.Name,
		Description: a.Description, Tags: tags, ContentType: a.ContentType, Category: string(a.Category),
		CurrentVersion: a.CurrentVersion, CreatedBy: a.CreatedBy, CreatedAt: a.CreatedAt.UTC(), UpdatedAt: a.UpdatedAt.UTC(),
		DeletedAt: timeOut(a.DeletedAt)}
}

type AssetPage struct {
	Assets        []Asset `json:"assets"`
	NextPageToken string  `json:"nextPageToken"`
}

func assetPageOut(p storage.AssetPage) AssetPage {
	return AssetPage{Assets: mapSlice(p.Assets, assetOut), NextPageToken: p.NextPageToken}
}

type Version struct {
	AssetID     string    `json:"assetId"`
	Number      int       `json:"number"`
	ContentType string    `json:"contentType"`
	SizeBytes   int64     `json:"sizeBytes"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
}

func versionOut(v storage.Version) Version {
	return Version{AssetID: v.AssetID, Number: v.Number, ContentType: v.ContentType, SizeBytes: v.SizeBytes,
		CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt.UTC()}
}

// Download is a presigned GET: the browser fetches the file from S3 itself.
type Download struct {
	URL       string    `json:"url"`
	Filename  string    `json:"filename"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func downloadOut(d storage.Download) Download {
	return Download{URL: d.URL, Filename: d.Filename, ExpiresAt: d.ExpiresAt.UTC()}
}

// UploadTicket is a presigned PUT: the browser sends the file to S3 with
// exactly these headers, then completes the upload.
type UploadTicket struct {
	UploadID  string            `json:"uploadId"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

// ---- Routes

func (s *Server) routesAssets(mux *http.ServeMux) {
	// Start an upload: a new asset (owner, or a project the asset is for), or
	// a new version of assetId.
	mux.Handle("POST /uploads", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Owner       *Owner   `json:"owner"`
			ProjectID   string   `json:"projectId"`
			AssetID     string   `json:"assetId"`
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Tags        []string `json:"tags"`
			FolderID    string   `json:"folderId"`
			ContentType string   `json:"contentType"`
			SizeBytes   int64    `json:"sizeBytes"`
		}](r)
		if err != nil {
			return nil, err
		}
		req := storage.UploadRequest{ProjectID: b.ProjectID, AssetID: b.AssetID, Name: b.Name, Description: b.Description,
			Tags: b.Tags, FolderID: b.FolderID, ContentType: b.ContentType, SizeBytes: b.SizeBytes}
		if b.Owner != nil {
			if req.Owner, err = b.Owner.in(); err != nil {
				return nil, err
			}
		}
		t, err := s.files.BeginUpload(r.Context(), actor, req)
		if err != nil {
			return nil, err
		}
		return UploadTicket{UploadID: t.UploadID, Method: t.Method, URL: t.URL, Headers: t.Headers, ExpiresAt: t.ExpiresAt.UTC()}, nil
	}))
	mux.Handle("POST /uploads/{id}/complete", serve(func(r *http.Request, actor string) (any, error) {
		a, err := s.files.CompleteUpload(r.Context(), actor, r.PathValue("id"))
		return assetOut(a), err
	}))

	// List assets: ?projectId= or ?ownerKind=&ownerId=, optionally ?tag=,
	// ?category=, ?folderId= (present but empty: the top level only),
	// ?trash=include|only, ?pageSize=, ?pageToken=.
	mux.Handle("GET /assets", serve(func(r *http.Request, actor string) (any, error) {
		q := r.URL.Query()
		query := storage.AssetQuery{ProjectID: q.Get("projectId"), Tag: q.Get("tag"),
			Category: storage.Category(q.Get("category")), PageToken: q.Get("pageToken")}
		if query.ProjectID == "" {
			owner, err := ownerQuery(r)
			if err != nil {
				return nil, err
			}
			query.Owner = owner
		}
		if q.Has("folderId") {
			folder := q.Get("folderId")
			query.Folder = &folder
		}
		switch q.Get("trash") {
		case "":
		case "include":
			query.IncludeDeleted = true
		case "only":
			query.OnlyDeleted = true
		default:
			return nil, badRequest("trash must be include or only")
		}
		size, err := queryInt(r, "pageSize")
		if err != nil {
			return nil, err
		}
		query.PageSize = size
		page, err := s.files.ListAssets(r.Context(), actor, query)
		return assetPageOut(page), err
	}))
	mux.Handle("GET /assets/{id}", serve(func(r *http.Request, actor string) (any, error) {
		a, err := s.files.GetAsset(r.Context(), actor, r.PathValue("id"))
		return assetOut(a), err
	}))
	// Change any of name, description, tags (the whole set) and folderId.
	mux.Handle("PATCH /assets/{id}", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Name        *string   `json:"name"`
			Description *string   `json:"description"`
			Tags        *[]string `json:"tags"`
			FolderID    *string   `json:"folderId"`
		}](r)
		if err != nil {
			return nil, err
		}
		a, err := s.files.UpdateAsset(r.Context(), actor, r.PathValue("id"),
			storage.AssetUpdate{Name: b.Name, Description: b.Description, Tags: b.Tags, FolderID: b.FolderID})
		return assetOut(a), err
	}))
	// To the trash; RestoreAsset brings it back until it's purged.
	mux.Handle("DELETE /assets/{id}", serve(func(r *http.Request, actor string) (any, error) {
		return nil, s.files.DeleteAsset(r.Context(), actor, r.PathValue("id"))
	}))
	mux.Handle("POST /assets/{id}/restore", serve(func(r *http.Request, actor string) (any, error) {
		a, err := s.files.RestoreAsset(r.Context(), actor, r.PathValue("id"))
		return assetOut(a), err
	}))
	mux.Handle("GET /assets/{id}/versions", serve(func(r *http.Request, actor string) (any, error) {
		vs, err := s.files.ListVersions(r.Context(), actor, r.PathValue("id"))
		return mapSlice(vs, versionOut), err
	}))
	// Make an earlier version current again.
	mux.Handle("POST /assets/{id}/revert", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Version int `json:"version"`
		}](r)
		if err != nil {
			return nil, err
		}
		a, err := s.files.RevertAsset(r.Context(), actor, r.PathValue("id"), b.Version)
		return assetOut(a), err
	}))
	// A presigned download; ?version= picks one (default: current).
	mux.Handle("GET /assets/{id}/download", serve(func(r *http.Request, actor string) (any, error) {
		v, err := queryInt(r, "version")
		if err != nil {
			return nil, err
		}
		d, err := s.files.DownloadURL(r.Context(), actor, r.PathValue("id"), v)
		return downloadOut(d), err
	}))
}
