package dam

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mjcourte/modulith/services/storage/internal/ledger"
)

// Folder errors returned by every Store (see FolderStore).
var (
	ErrFolderNotFound = errors.New("storage: folder not found")
	ErrInvalidFolder  = errors.New("storage: invalid folder")
	ErrFolderNotEmpty = errors.New("storage: folder not empty")
	ErrNameTaken      = errors.New("storage: name already used in this folder")
)

// MaxFolderDepth bounds how deeply folders nest (a top-level folder is depth
// 1). It keeps trees navigable and the cycle check on move cheap. Folder
// names share MaxNameLen with assets.
const MaxFolderDepth = 32

// Folder groups assets in one owner's library. Folders form a tree per owner,
// stored as an adjacency list (each folder knows its parent). The tree exists
// only here: S3 keys never change when folders are created, renamed or moved.
type Folder struct {
	ID        string       // UUIDv7
	Owner     ledger.Owner // folders and the assets in them share an owner
	ParentID  string       // "" means top level; kept when trashed, so restore puts it back
	Name      string       // unique among live siblings, ignoring case; stored as typed
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time  // last rename or move
	DeletedAt *time.Time // nil until moved to the trash
	// DeletionID groups everything one DeleteFolderTree trashed (see Asset.DeletionID).
	DeletionID string
}

// FolderDeletion reports what a delete moved to the trash.
type FolderDeletion struct {
	DeletionID string // shared by every folder and asset trashed by this call
	Folders    int    // folders trashed, including the one named
	Assets     int    // live assets trashed
}

// FolderNotEmptyError is returned by DeleteFolder when the folder still holds
// live content. It matches ErrFolderNotEmpty with errors.Is, and carries the
// counts so a UI can ask "Delete 'Brand' and its 3 folders and 12 files?" and,
// if confirmed, call DeleteFolderTree. Get it with errors.As.
type FolderNotEmptyError struct {
	Folders int // live sub-folders, at any depth
	Assets  int // live assets, at any depth
}

func (e *FolderNotEmptyError) Error() string {
	return fmt.Sprintf("%v: contains %d folders and %d assets", ErrFolderNotEmpty, e.Folders, e.Assets)
}

// Unwrap lets errors.Is(err, ErrFolderNotEmpty) match this error.
func (e *FolderNotEmptyError) Unwrap() error { return ErrFolderNotEmpty }

// FolderStore persists folders. Like Store it is pure data: shape and tree
// rules only, no authorization.
//
// Rules every implementation enforces:
//   - a parent must exist, be live and have the same owner, or ErrFolderNotFound
//   - live sibling names are unique ignoring case ("Logos" and "logos" clash), or ErrNameTaken
//   - names are portable across Windows, macOS and Linux (see folders_rules.go), or ErrInvalidFolder
//   - nesting is at most MaxFolderDepth deep, or ErrInvalidFolder
//
// Deleting moves folders to the trash (soft delete), like assets, keeping
// ParentID so a restore rebuilds the tree. Trashed folders stay readable with
// GetFolder but are otherwise invisible: not listed, never a valid parent or
// destination, never clash on names, and can't be renamed or moved
// (ErrFolderNotFound). A purge job removes them for good later.
type FolderStore interface {
	// CreateFolder stores a new folder, assigning its timestamps, and its ID
	// unless f.ID is set: then an already stored folder with that ID is
	// returned unchanged, as CreateAsset does.
	CreateFolder(ctx context.Context, f Folder) (Folder, error)
	// GetFolder returns the folder, including trashed ones, or ErrFolderNotFound.
	GetFolder(ctx context.Context, id string) (Folder, error)
	// ListFolders returns the live direct children of parentID ("" for the top
	// level) in owner's tree, sorted by name ignoring case.
	ListFolders(ctx context.Context, owner ledger.Owner, parentID string) ([]Folder, error)
	// RenameFolder changes the folder's name, subject to the sibling rule.
	RenameFolder(ctx context.Context, id, name string) (Folder, error)
	// MoveFolder gives the folder a new parent ("" for the top level). Moving a
	// folder into itself or one of its own descendants would make a cycle and
	// is ErrInvalidFolder; so is a move that would exceed MaxFolderDepth.
	MoveFolder(ctx context.Context, id, newParentID string) (Folder, error)
	// DeleteFolder trashes an empty folder: one with no live sub-folders and no
	// live assets. Otherwise it returns a *FolderNotEmptyError saying what's in
	// the way. Deleting a trashed folder again is a no-op.
	DeleteFolder(ctx context.Context, id string) (FolderDeletion, error)
	// DeleteFolderTree is rm -rf: it trashes the folder, every live sub-folder
	// at any depth and every live asset in them, all at once (one transaction
	// in Postgres), under a single DeletionID. Nothing is destroyed: assets'
	// bytes stay until the purge job, and the whole tree can be restored by
	// DeletionID. Deleting a trashed folder again is a no-op.
	DeleteFolderTree(ctx context.Context, id string) (FolderDeletion, error)

	// FolderPath returns the folder and its ancestors, top level first: the
	// breadcrumbs for a file browser. Works for trashed folders too.
	FolderPath(ctx context.Context, id string) ([]Folder, error)
	// ListTrashedFolders returns owner's trashed folders, most recently
	// deleted first.
	ListTrashedFolders(ctx context.Context, owner ledger.Owner) ([]Folder, error)
	// RestoreDeletion undoes one delete: every folder and asset trashed under
	// deletionID comes back where it was. A folder whose parent is no longer
	// live lands at the top level; a folder whose name is now taken by a live
	// sibling is renamed "Name (2)", "Name (3)", ... Returns what was restored
	// (Folders/Assets counts; nothing left to restore is not an error).
	RestoreDeletion(ctx context.Context, deletionID string) (FolderDeletion, error)
}

// uniqueFolderName returns name, or the first of "name (2)", "name (3)", ...
// that taken reports free. Used when a restore or an ownership transfer would
// otherwise put two live folders with the same name side by side.
func uniqueFolderName(name string, taken func(string) bool) string {
	if !taken(name) {
		return name
	}
	for n := 2; ; n++ {
		if candidate := fmt.Sprintf("%s (%d)", name, n); !taken(candidate) {
			return candidate
		}
	}
}
