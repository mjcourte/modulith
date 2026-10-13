package admin

import (
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"gyoza/services/storage"
)

func (s *server) listAssets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	assets, err := s.q.assets(r.Context(), q.Get("owner_kind"), q.Get("owner_id"), q.Get("folder_id"), q.Get("q"), q.Get("trashed") == "on")
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "assets", "Assets", assets)
}

type assetPage struct {
	Asset    AssetRow
	Versions []VersionRow
	Uses     []UseRow
	// Projects the actor may write, for "use in project" (empty without an actor).
	Projects []WritableProject
}

func (s *server) showAsset(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	var p assetPage
	var err error
	if p.Asset, err = s.q.asset(ctx, id); err != nil {
		s.fail(w, err)
		return
	}
	if p.Versions, err = s.q.versions(ctx, id); err == nil {
		if p.Uses, err = s.q.uses(ctx, "", id); err == nil && actorOf(r) != "" {
			p.Projects, err = s.q.writableProjects(ctx, actorOf(r))
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "asset", p.Asset.Name, p)
}

// downloadAsset redirects to a presigned GET (version 0 is the current one).
func (s *server) downloadAsset(w http.ResponseWriter, r *http.Request) {
	a, err := actor(r)
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	v, _ := strconv.Atoi(r.URL.Query().Get("v"))
	d, err := s.storage.DownloadURL(r.Context(), a, r.PathValue("id"), v)
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	http.Redirect(w, r, d.URL, http.StatusFound)
}

// maxUpload bounds what the admin buffers per upload.
const maxUpload = 1 << 30

// uploadAsset plays the browser's part of the two-phase upload, server-side
// so the bucket needs no CORS rules: BeginUpload, PUT to the presigned URL,
// CompleteUpload.
func (s *server) uploadAsset(w http.ResponseWriter, r *http.Request) {
	a, err := actor(r)
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil { // larger files spill to temp files
		done(w, r, "", err, "")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" || contentType == "application/octet-stream" {
		if byExt := mime.TypeByExtension(filepath.Ext(header.Filename)); byExt != "" {
			contentType = byExt
		}
	}
	req := storage.UploadRequest{
		AssetID:     strings.TrimSpace(r.FormValue("asset_id")),
		ContentType: contentType,
		SizeBytes:   header.Size,
	}
	if req.AssetID == "" { // a new asset; a new version takes these from the asset
		req.Owner = owner(r)
		req.ProjectID = strings.TrimSpace(r.FormValue("project_id"))
		req.FolderID = strings.TrimSpace(r.FormValue("folder_id"))
		req.Name = strings.TrimSpace(r.FormValue("name"))
		if req.Name == "" {
			req.Name = header.Filename
		}
		if tags := strings.TrimSpace(r.FormValue("tags")); tags != "" {
			req.Tags = strings.Split(tags, ",")
		}
	}

	ticket, err := s.storage.BeginUpload(r.Context(), a, req)
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	put, err := http.NewRequestWithContext(r.Context(), ticket.Method, ticket.URL, file)
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	put.ContentLength = header.Size
	for k, v := range ticket.Headers {
		put.Header.Set(k, v)
	}
	resp, err := s.http.Do(put)
	if err != nil {
		done(w, r, "", fmt.Errorf("PUT to S3: %w", err), "")
		return
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		done(w, r, "", fmt.Errorf("PUT to S3: %s", resp.Status), "")
		return
	}
	asset, err := s.storage.CompleteUpload(r.Context(), a, ticket.UploadID)
	if err != nil {
		done(w, r, "", err, "")
		return
	}
	done(w, r, "/assets/"+asset.ID, nil, fmt.Sprintf("uploaded version %d", asset.CurrentVersion))
}

func (s *server) deleteAsset(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "moved to trash", func(a string) error {
		return s.storage.DeleteAsset(r.Context(), a, r.PathValue("id"))
	})
}

func (s *server) restoreAsset(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "restored", func(a string) error {
		_, err := s.storage.RestoreAsset(r.Context(), a, r.PathValue("id"))
		return err
	})
}

func (s *server) revertAsset(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "reverted", func(a string) error {
		v, err := strconv.Atoi(r.FormValue("version"))
		if err != nil {
			return err
		}
		_, err = s.storage.RevertAsset(r.Context(), a, r.PathValue("id"), v)
		return err
	})
}

type foldersPage struct {
	OwnerKind, OwnerID string
	Folders            []FolderRow
}

func (s *server) listFolders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := foldersPage{OwnerKind: q.Get("owner_kind"), OwnerID: q.Get("owner_id")}
	if p.OwnerKind != "" {
		var err error
		if p.Folders, err = s.q.folders(r.Context(), p.OwnerKind, p.OwnerID); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.render(w, r, "folders", "Folders", p)
}

func (s *server) listObjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	objects, err := s.q.objects(r.Context(), q.Get("owner_kind"), q.Get("owner_id"), q.Get("q"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "objects", "Ledger objects", objects)
}

func (s *server) listUploads(w http.ResponseWriter, r *http.Request) {
	uploads, err := s.q.uploads(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, r, "uploads", "Uploads", uploads)
}

// useRef reads the project and the asset version a use form names. The
// project pickers send "assetID:version" in asset; the asset page sends
// asset_id and version separately.
func useRef(r *http.Request) (string, storage.AssetRef, error) {
	id, v := r.FormValue("asset_id"), r.FormValue("version")
	if a, ver, ok := strings.Cut(r.FormValue("asset"), ":"); ok {
		id, v = a, ver
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return "", storage.AssetRef{}, fmt.Errorf("version: %w", err)
	}
	return r.FormValue("project_id"), storage.AssetRef{AssetID: id, Version: n}, nil
}

func (s *server) recordUse(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "use recorded", func(a string) error {
		project, ref, err := useRef(r)
		if err != nil {
			return err
		}
		return s.storage.RecordAssetUse(r.Context(), a, project, ref)
	})
}

func (s *server) forgetUse(w http.ResponseWriter, r *http.Request) {
	withActor(w, r, "use forgotten", func(a string) error {
		project, ref, err := useRef(r)
		if err != nil {
			return err
		}
		return s.storage.ForgetAssetUse(r.Context(), a, project, ref)
	})
}
