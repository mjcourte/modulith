package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"gyoza/services/storage"
)

// maxBody caps request bodies: they carry metadata only (files go straight
// to S3 by presigned URL).
const maxBody = 1 << 20

// errBadRequest is a request the gateway itself can't read: malformed JSON,
// a bad query parameter. 400.
var errBadRequest = errors.New("gateway: bad request")

func badRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errBadRequest, fmt.Sprintf(format, args...))
}

// decode reads a JSON body into a T. Unknown fields are refused, so a typo
// in a field name fails loudly instead of being ignored.
func decode[T any](r *http.Request) (T, error) {
	var v T
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, badRequest("body: %v", err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return v, badRequest("body: more than one JSON value")
	}
	return v, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// queryInt reads an optional integer query parameter; missing is 0.
func queryInt(r *http.Request, name string) (int, error) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, badRequest("%s must be a non-negative integer", name)
	}
	return n, nil
}

// ---- Shared wire types

// Owner is who owns files or a project. Browsers may name users, orgs and
// projects; the gyoza owner (templates) is service-to-service only.
type Owner struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func ownerOut(o storage.Owner) Owner { return Owner{Kind: string(o.Kind), ID: o.ID} }

func (o Owner) in() (storage.Owner, error) {
	switch k := storage.OwnerKind(o.Kind); k {
	case storage.OwnerUser, storage.OwnerOrg, storage.OwnerProject:
		return storage.Owner{Kind: k, ID: o.ID}, nil
	}
	return storage.Owner{}, badRequest("owner kind %q: want user, org or project", o.Kind)
}

// ownerQuery reads an owner from ?ownerKind=&ownerId=.
func ownerQuery(r *http.Request) (storage.Owner, error) {
	q := r.URL.Query()
	return Owner{Kind: q.Get("ownerKind"), ID: q.Get("ownerId")}.in()
}

// timeOut formats an optional time; nil stays null in JSON.
func timeOut(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// BadRequest is badRequest for the WebSocket, whose messages the gateway
// reads itself too.
func BadRequest(format string, args ...any) error { return badRequest(format, args...) }
