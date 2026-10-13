// Command admin serves the development admin UI (apps/admin) next to a
// running gyoza: it reads the database directly and makes changes over gRPC.
// Dev only: it lets anyone act as any user.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"gyoza/apps/admin"
	"gyoza/platform/config"
	"gyoza/platform/postgres"
	"gyoza/platform/shutdown"
	"gyoza/services/documents"
	"gyoza/services/iam"
	"gyoza/services/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("admin failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env := config.FromEnv()
	enabled := env.Bool("GYOZA_ADMIN", false)
	dbURL := env.Required("GYOZA_DATABASE_URL")
	addr := env.String("GYOZA_ADMIN_ADDR", "localhost:8081")
	// The services it calls: a monolith serves both on one address.
	iamAddr := env.String("GYOZA_ADMIN_IAM_ADDR", "localhost:9090")
	storageAddr := env.String("GYOZA_ADMIN_STORAGE_ADDR", "localhost:9090")
	docsAddr := env.String("GYOZA_ADMIN_DOCUMENTS_ADDR", "localhost:9090")
	shutdownTimeout := env.Duration("GYOZA_SHUTDOWN_TIMEOUT", 20*time.Second)
	if err := env.Err(); err != nil {
		return err
	}
	if !enabled {
		return errors.New("the admin UI lets anyone act as any user: set GYOZA_ADMIN=1 to run it (development only)")
	}

	pool, err := postgres.Connect(ctx, dbURL, "gyoza-admin")
	if err != nil {
		return err
	}
	// grpc.NewClient doesn't dial yet, so these can't fail on a server that
	// isn't up; calls fail until it is.
	creds := grpc.WithTransportCredentials(insecure.NewCredentials())
	iamConn, err := grpc.NewClient(iamAddr, creds)
	if err != nil {
		pool.Close()
		return err
	}
	defer iamConn.Close()
	storageConn, err := grpc.NewClient(storageAddr, creds)
	if err != nil {
		pool.Close()
		return err
	}
	defer storageConn.Close()
	docsConn, err := grpc.NewClient(docsAddr, creds)
	if err != nil {
		pool.Close()
		return err
	}
	defer docsConn.Close()

	handler, err := admin.New(pool, iam.NewGRPCClient(iamConn), storage.NewGRPCClient(storageConn), documents.NewGRPCClient(docsConn))
	if err != nil {
		pool.Close()
		return err
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		pool.Close()
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(lis) }()
	slog.Info("admin started", "url", "http://"+lis.Addr().String(), "iam", iamAddr, "storage", storageAddr, "documents", docsAddr)

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		pool.Close()
		return fmt.Errorf("http server: %w", err)
	}
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Warn("admin requests cut off at shutdown deadline")
	}
	return shutdown.Within(shutdownCtx, pool.Close)
}
