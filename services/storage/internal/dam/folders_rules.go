package dam

// Folder rules shared by every FolderStore implementation, so Memory and
// Postgres accept and reject exactly the same folders.

import (
	"fmt"
	"slices"
	"strings"
)

// Folder names must work as directory names everywhere a library might be
// exported or synced, so they follow the strictest common rules (Windows'):
//   - none of < > : " / \ | ? * and no control characters
//   - no trailing dot (which also rules out "." and "..")
//   - not a reserved Windows device name (CON, NUL, COM1, ...), with or without an extension
//
// Asset names are display names and aren't restricted this way; downloads
// sanitise filenames separately.
const ForbiddenNameChars = `<>:"/\|?*`

var reservedFolderNames = []string{
	"con", "prn", "aux", "nul",
	"com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9",
	"lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9",
}

// IsReservedName reports whether name is a reserved Windows device name, with
// or without an extension ("CON", "con.txt").
func IsReservedName(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	return slices.Contains(reservedFolderNames, strings.ToLower(base))
}

// normalizeFolderName trims a folder name and checks it against the shared
// name limits and the portability rules above.
func normalizeFolderName(name string) (string, error) {
	name, err := normalizeName(name, ErrInvalidFolder)
	if err != nil {
		return "", err
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(ForbiddenNameChars, r) {
			return "", fmt.Errorf("%w: name %q contains %q", ErrInvalidFolder, name, r)
		}
	}
	if strings.HasSuffix(name, ".") {
		return "", fmt.Errorf("%w: name %q ends with a dot", ErrInvalidFolder, name)
	}
	// "CON.txt" is just as reserved as "CON" on Windows.
	if IsReservedName(name) {
		return "", fmt.Errorf("%w: %q is a reserved name", ErrInvalidFolder, name)
	}
	return name, nil
}

// folderNameKey is the form of a name used to compare siblings: lowercased,
// so "Logos" and "logos" clash. Postgres will store this key in its own column
// with a unique index, so Go decides what counts as "the same name" in every
// implementation (rather than Go's strings.EqualFold here and SQL's lower()
// there, which differ for a few characters).
//
// Simple per-character lowercasing matches how Windows and macOS compare
// names; full Unicode case folding would also merge "ß" with "ss", which
// those filesystems treat as different.
func folderNameKey(name string) string {
	return strings.ToLower(name)
}

// normalize validates a new folder for CreateFolder: owner shape, name rules,
// and a well-formed parent ID. Whether the parent exists, its owner, sibling
// names and depth need stored data, so each Store checks those.
func (f Folder) normalize() (Folder, error) {
	if err := f.Owner.Validate(); err != nil {
		return f, fmt.Errorf("%w: %w", ErrInvalidFolder, err)
	}
	var err error
	if f.Name, err = normalizeFolderName(f.Name); err != nil {
		return f, err
	}
	if err := checkOptionalID(f.ParentID, "parent folder", ErrInvalidFolder); err != nil {
		return f, err
	}
	return f, nil
}

// checkFolderDepth enforces MaxFolderDepth for a folder that would sit at depth.
func checkFolderDepth(depth int) error {
	if depth > MaxFolderDepth {
		return fmt.Errorf("%w: would be nested %d deep, max %d", ErrInvalidFolder, depth, MaxFolderDepth)
	}
	return nil
}
