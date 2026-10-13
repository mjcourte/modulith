package documents

// Setup shared by the binaries hosting documents (cmd/gyoza, cmd/documents),
// so they can't drift apart.

import (
	"context"
	"crypto/rand"
	"log/slog"
	"os"
	"time"

	"gyoza/internal/engine"
	"gyoza/platform/config"
	gnats "gyoza/platform/nats"
	"gyoza/services/documents/internal/live"
	"gyoza/services/documents/internal/manager"
	"gyoza/services/documents/internal/snapshots"
	"gyoza/services/documents/internal/wal"
	"gyoza/services/storage"
)

// Settings is everything documents reads from the environment.
type Settings struct {
	Manager  Config
	NATSURL  string // comma-separated cluster members
	Consumer string // the durable consumer's name, shared by every replica
	// MaxSnapshotBytes bounds snapshot downloads (storage refuses bigger
	// uploads; the same setting).
	MaxSnapshotBytes int64
}

// SettingsFromEnv reads documents' settings. Problems are recorded in env;
// check env.Err() after reading everything.
func SettingsFromEnv(env *config.Env) Settings {
	return Settings{
		Manager: Config{
			Replica: env.String("GYOZA_DOCUMENTS_REPLICA", defaultReplica()),
			Live: live.Config{
				Debounce: env.Duration("GYOZA_DOCUMENTS_DEBOUNCE", 2*time.Second),
				MaxDelay: env.Duration("GYOZA_DOCUMENTS_MAX_DELAY", 30*time.Second),
				TTL:      env.Duration("GYOZA_DOCUMENTS_TTL", 10*time.Minute),
			},
		},
		NATSURL:          env.Required("GYOZA_NATS_URL"),
		Consumer:         env.String("GYOZA_DOCUMENTS_CONSUMER", "documents"),
		MaxSnapshotBytes: env.PositiveInt("GYOZA_SNAPSHOT_MAX_BYTES", storage.DefaultMaxSnapshotBytes),
	}
}

// defaultReplica is unique per running instance: the host name plus a random
// suffix, so a restarted process is a new replica too (its stamps then never
// collide with the old one's).
func defaultReplica() string {
	host, err := os.Hostname()
	if err != nil {
		host = "documents"
	}
	return host + "-" + rand.Text()[:8]
}

// Open builds documents on its dependencies: storage (snapshots and asset
// uses; in-process or a gRPC client), iam's authorizer (in-process or a gRPC
// client) and NATS. The engine is the mock until the native one exists.
// Close the Manager at shutdown, before NATS and storage go: it saves every
// open document.
func Open(s Settings, st storage.Service, iamAuthz storage.Authorizer, nc *gnats.Client) *Manager {
	cfg := s.Manager
	cfg.WAL = wal.New(nc.JS)
	store := snapshots.NewStorageStore(st, snapshots.NewHTTPTransfer(s.MaxSnapshotBytes))
	return manager.New(engine.Mock{}, store, NewIAMAuthorizer(iamAuthz), storageUses{st}, cfg)
}

// SnapshotTransfer moves snapshot bytes to and from presigned URLs.
type SnapshotTransfer = snapshots.Transfer

// OpenLocal builds documents like Open but without NATS: one instance alone,
// nothing distributed. For tests of what is built on documents (the
// gateway's), over storage.NewMemory and a transfer adapting it.
func OpenLocal(st storage.Service, t SnapshotTransfer, iamAuthz storage.Authorizer) *Manager {
	store := snapshots.NewStorageStore(st, t)
	return manager.New(engine.Mock{}, store, NewIAMAuthorizer(iamAuthz), storageUses{st}, Config{Replica: "local"})
}

// StartConsumer runs documents' one NATS consumer (every project's deltas)
// in the background until ctx is done. The returned channel closes once it
// has stopped, after finishing the message in hand.
func StartConsumer(ctx context.Context, m *Manager, nc *gnats.Client, durable string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := wal.New(nc.JS).Consume(ctx, wal.ConsumerConfig{Durable: durable}, m.ApplyDelta,
			func(err error) { slog.Error("documents: delta", "err", err) })
		if err != nil && ctx.Err() == nil {
			slog.Error("documents: consumer stopped", "err", err)
		}
	}()
	return done
}

// storageUses is documents' AssetUses, answered by storage.
type storageUses struct{ s storage.Service }

func (u storageUses) RecordAssetUse(ctx context.Context, actorID, projectID string, ref engine.AssetRef) error {
	return u.s.RecordAssetUse(ctx, actorID, projectID, storage.AssetRef{AssetID: ref.ID, Version: ref.Version})
}

func (u storageUses) ForgetAssetUse(ctx context.Context, actorID, projectID string, ref engine.AssetRef) error {
	return u.s.ForgetAssetUse(ctx, actorID, projectID, storage.AssetRef{AssetID: ref.ID, Version: ref.Version})
}
