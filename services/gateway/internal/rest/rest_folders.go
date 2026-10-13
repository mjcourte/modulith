package rest

import (
	"net/http"
	"time"

	"gyoza/services/storage"
)

// ---- Wire types

type Folder struct {
	ID        string     `json:"id"`
	Owner     Owner      `json:"owner"`
	ParentID  string     `json:"parentId"` // "" is the top level
	Name      string     `json:"name"`
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	DeletedAt *time.Time `json:"deletedAt"` // set while in the trash
}

func folderOut(f storage.Folder) Folder {
	return Folder{ID: f.ID, Owner: ownerOut(f.Owner), ParentID: f.ParentID, Name: f.Name, CreatedBy: f.CreatedBy,
		CreatedAt: f.CreatedAt.UTC(), UpdatedAt: f.UpdatedAt.UTC(), DeletedAt: timeOut(f.DeletedAt)}
}

// FolderDeletion is what a delete or restore moved: every folder and asset
// it touched shares DeletionID.
type FolderDeletion struct {
	DeletionID string `json:"deletionId"`
	Folders    int    `json:"folders"`
	Assets     int    `json:"assets"`
}

func folderDeletionOut(d storage.FolderDeletion) FolderDeletion {
	return FolderDeletion{DeletionID: d.DeletionID, Folders: d.Folders, Assets: d.Assets}
}

// ---- Routes

func (s *Server) routesFolders(mux *http.ServeMux) {
	// An owner's folder: ?ownerKind=&ownerId=, ?folderId= (empty: the top
	// level), ?pageToken= (pages the assets; sub-folders come on the first).
	mux.Handle("GET /folders", serve(func(r *http.Request, actor string) (any, error) {
		owner, err := ownerQuery(r)
		if err != nil {
			return nil, err
		}
		q := r.URL.Query()
		c, err := s.files.ListFolderContents(r.Context(), actor, owner, q.Get("folderId"), q.Get("pageToken"))
		return struct {
			Path    []Folder  `json:"path"` // breadcrumbs, top level first
			Folders []Folder  `json:"folders"`
			Assets  AssetPage `json:"assets"`
		}{mapSlice(c.Path, folderOut), mapSlice(c.Folders, folderOut), assetPageOut(c.Assets)}, err
	}))
	mux.Handle("POST /folders", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Owner    Owner  `json:"owner"`
			ParentID string `json:"parentId"`
			Name     string `json:"name"`
		}](r)
		if err != nil {
			return nil, err
		}
		owner, err := b.Owner.in()
		if err != nil {
			return nil, err
		}
		f, err := s.files.CreateFolder(r.Context(), actor, storage.Folder{Owner: owner, ParentID: b.ParentID, Name: b.Name})
		return folderOut(f), err
	}))
	// Rename and/or move (parentId "" is the top level). A move comes first,
	// so a rename that fails after it leaves the folder moved; the error says
	// which.
	mux.Handle("PATCH /folders/{id}", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Name     *string `json:"name"`
			ParentID *string `json:"parentId"`
		}](r)
		if err != nil {
			return nil, err
		}
		if b.Name == nil && b.ParentID == nil {
			return nil, badRequest("nothing to change: give name and/or parentId")
		}
		id := r.PathValue("id")
		var f storage.Folder
		if b.ParentID != nil {
			if f, err = s.files.MoveFolder(r.Context(), actor, id, *b.ParentID); err != nil {
				return nil, err
			}
		}
		if b.Name != nil {
			if f, err = s.files.RenameFolder(r.Context(), actor, id, *b.Name); err != nil {
				return nil, err
			}
		}
		return folderOut(f), nil
	}))
	// To the trash. An empty folder only, unless ?tree=true (then everything
	// in it): a non-empty one is 409 FOLDER_NOT_EMPTY with its counts.
	mux.Handle("DELETE /folders/{id}", serve(func(r *http.Request, actor string) (any, error) {
		del := s.files.DeleteFolder
		switch r.URL.Query().Get("tree") {
		case "", "false":
		case "true":
			del = s.files.DeleteFolderTree
		default:
			return nil, badRequest("tree must be true or false")
		}
		d, err := del(r.Context(), actor, r.PathValue("id"))
		return folderDeletionOut(d), err
	}))
	mux.Handle("POST /folders/{id}/restore", serve(func(r *http.Request, actor string) (any, error) {
		d, err := s.files.RestoreFolder(r.Context(), actor, r.PathValue("id"))
		return folderDeletionOut(d), err
	}))
	mux.Handle("GET /folders/{id}/path", serve(func(r *http.Request, actor string) (any, error) {
		path, err := s.files.FolderPath(r.Context(), actor, r.PathValue("id"))
		return mapSlice(path, folderOut), err
	}))

	// An owner's trash: ?ownerKind=&ownerId=, ?pageToken=. uses counts the
	// projects still using each trashed asset (absent: none), so the UI can
	// warn before the purge.
	mux.Handle("GET /trash", serve(func(r *http.Request, actor string) (any, error) {
		owner, err := ownerQuery(r)
		if err != nil {
			return nil, err
		}
		t, err := s.files.ListTrash(r.Context(), actor, owner, r.URL.Query().Get("pageToken"))
		uses := t.Uses
		if uses == nil {
			uses = map[string]int{}
		}
		return struct {
			Folders []Folder       `json:"folders"`
			Assets  AssetPage      `json:"assets"`
			Uses    map[string]int `json:"uses"`
		}{mapSlice(t.Folders, folderOut), assetPageOut(t.Assets), uses}, err
	}))
}
