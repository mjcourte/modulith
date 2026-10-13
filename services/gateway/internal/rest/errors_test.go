package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gyoza/services/storage"
)

func errorResponse(t *testing.T, err error) (int, errorDetail) {
	t.Helper()
	rec := httptest.NewRecorder()
	writeError(rec, httptest.NewRequest("GET", "/x", nil), err)
	var body errorBody
	if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
		t.Fatalf("error body isn't JSON: %v: %s", e, rec.Body)
	}
	return rec.Code, body.Error
}

// Every mapped sentinel, wrapped as services wrap them, answers with its own
// status and code: a row shadowed by an earlier one (two sentinels that are
// the same value, or one wrapping the other) would fail here.
func TestErrorTablesMapEverySentinel(t *testing.T) {
	for _, table := range errorTables {
		for _, row := range table {
			status, d := errorResponse(t, fmt.Errorf("context: %w", row.err))
			if status != row.status || d.Code != row.code {
				t.Errorf("%v: got %d %s, want %d %s", row.err, status, d.Code, row.status, row.code)
			}
			if status >= 500 {
				t.Errorf("%v is mapped to a 5xx: map it to what the caller did wrong, or leave it out", row.err)
			}
		}
	}
}

// An unmapped error is a 500 whose text, which may hold internals (SQL,
// hosts), stays in the log.
func TestErrorUnmappedIsGeneric500(t *testing.T) {
	status, d := errorResponse(t, errors.New("pq: connection to 10.0.0.5 refused"))
	if status != http.StatusInternalServerError || d.Code != "INTERNAL" || strings.Contains(d.Message, "10.0.0.5") {
		t.Errorf("got %d %s %q, want a generic 500", status, d.Code, d.Message)
	}
}

func TestErrorFolderNotEmptyCarriesCounts(t *testing.T) {
	status, d := errorResponse(t, fmt.Errorf("delete: %w", &storage.FolderNotEmptyError{Folders: 2, Assets: 5}))
	if status != http.StatusConflict || d.Folders == nil || *d.Folders != 2 || d.Assets == nil || *d.Assets != 5 {
		t.Errorf("got %d %+v, want 409 with 2 folders and 5 assets", status, d)
	}
}
