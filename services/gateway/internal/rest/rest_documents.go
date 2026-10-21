package rest

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/mjcourte/modulith/internal/engine"
	"github.com/mjcourte/modulith/services/gateway/internal/session"
	"github.com/mjcourte/modulith/services/storage"
)

// AssetRef pins an asset version, as documents use them.
type AssetRef struct {
	AssetID string `json:"assetId"`
	Version int    `json:"version"`
}

func (s *Server) routesDocuments(mux *http.ServeMux) {
	// The document as engine bytes (the latest state, created if the project
	// has none), with what a client needs to apply live deltas after it:
	// Modulith-Format and Modulith-Version (the wire format), Modulith-Seq (the last
	// delta stream sequence it covers).
	mux.HandleFunc("GET /projects/{id}/document", func(w http.ResponseWriter, r *http.Request) {
		d, err := s.docs.GetDocument(r.Context(), session.Actor(r.Context()), r.PathValue("id"))
		if err != nil {
			writeError(w, r, err)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Modulith-Format", string(d.Snapshot.Format))
		h.Set("Modulith-Version", strconv.Itoa(d.Snapshot.Version))
		h.Set("Modulith-Seq", strconv.FormatUint(d.Seq, 10))
		w.Write(d.Snapshot.Payload)
	})
	// Part of the document as JSON (the engine's query formats). The engine's
	// view is returned as is under "data".
	mux.Handle("POST /projects/{id}/document/query", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Format    string         `json:"format"`
			Source    string         `json:"source"`
			Variables map[string]any `json:"variables"`
		}](r)
		if err != nil {
			return nil, err
		}
		v, err := s.docs.QueryDocument(r.Context(), actor, r.PathValue("id"),
			engine.Query{Format: engine.QueryFormat(b.Format), Source: b.Source, Variables: b.Variables})
		if err != nil {
			return nil, err
		}
		return struct {
			Data json.RawMessage `json:"data"`
		}{v.Data}, nil
	}))

	// An asset version the project's document uses: its details, the version
	// and a download, also while it's in the trash (deletedAt set).
	mux.Handle("GET /projects/{id}/refs/{assetId}/{version}", serve(func(r *http.Request, actor string) (any, error) {
		v, err := strconv.Atoi(r.PathValue("version"))
		if err != nil || v < 1 {
			return nil, badRequest("version must be a positive integer")
		}
		res, err := s.files.ResolveRef(r.Context(), actor, r.PathValue("id"), storage.AssetRef{AssetID: r.PathValue("assetId"), Version: v})
		if err != nil {
			return nil, err
		}
		return struct {
			Asset    Asset    `json:"asset"`
			Version  Version  `json:"version"`
			Download Download `json:"download"`
		}{assetOut(res.Asset), versionOut(res.Version), downloadOut(res.Download)}, nil
	}))

	// Fork an asset the project uses into the project (or its owner, target)
	// and point the document at the fork, in one call. If the fork worked but
	// the document wasn't rewritten, the fork comes back with rewriteError:
	// repeat the call (forking the same version again returns the same copy).
	mux.Handle("POST /projects/{id}/forks", serve(func(r *http.Request, actor string) (any, error) {
		b, err := decode[struct {
			Ref    AssetRef `json:"ref"`
			Target *Owner   `json:"target"` // default: the project
			Via    string   `json:"via"`    // a project using it, when the actor can't read it otherwise
		}](r)
		if err != nil {
			return nil, err
		}
		var target storage.Owner
		if b.Target != nil {
			if target, err = b.Target.in(); err != nil {
				return nil, err
			}
		}
		projectID := r.PathValue("id")
		from := storage.AssetRef{AssetID: b.Ref.AssetID, Version: b.Ref.Version}
		fork, err := s.files.ForkAsset(r.Context(), actor, projectID, from, target, b.Via)
		if err != nil {
			return nil, err
		}
		out := struct {
			Asset        Asset  `json:"asset"`
			RewriteError string `json:"rewriteError,omitempty"`
		}{Asset: assetOut(fork)}
		_, err = s.docs.ReplaceAssetRef(r.Context(), actor, projectID,
			engine.AssetRef{ID: from.AssetID, Version: from.Version},
			engine.AssetRef{ID: fork.ID, Version: fork.CurrentVersion})
		if err != nil {
			out.RewriteError = err.Error()
		}
		return out, nil
	}))

	// A presigned download of one of the project's stored snapshots (the
	// object a snapshot pointer names). Storage's snapshot calls are
	// service-to-service and don't authorize, so the actor's read access is
	// checked here first.
	mux.Handle("GET /projects/{id}/snapshots/{objectId}", serve(func(r *http.Request, actor string) (any, error) {
		projectID := r.PathValue("id")
		project := storage.Owner{Kind: storage.OwnerProject, ID: projectID}
		if err := s.authz.CanRead(r.Context(), actor, project, projectID); err != nil {
			return nil, err
		}
		d, err := s.files.SnapshotURL(r.Context(), projectID, r.PathValue("objectId"))
		return downloadOut(d), err
	}))
}
