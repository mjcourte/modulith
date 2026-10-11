package storageledger_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "gyoza/contracts/gen/go/gyoza/storageledger/v1"
	ledger "gyoza/services/storageledger"
)

// TestGRPC runs the shared suite through the gRPC client and server, so the
// remote Service behaves exactly like an in-process one: same results, same
// errors. Memory sits behind the server; the adapters are what's under test.
func TestGRPC(t *testing.T) {
	runServiceTests(t, func(t *testing.T) ledger.Service {
		lis := bufconn.Listen(1 << 20)
		srv := grpc.NewServer()
		pb.RegisterLedgerServiceServer(srv, ledger.NewGRPCServer(ledger.NewMemory()))
		go srv.Serve(lis)
		t.Cleanup(srv.Stop)

		conn, err := grpc.NewClient("passthrough:///bufconn",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { conn.Close() })
		return ledger.NewGRPCClient(conn)
	})
}
