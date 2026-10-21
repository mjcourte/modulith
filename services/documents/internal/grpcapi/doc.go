// Package grpcapi serves documents over gRPC (DocumentsService in
// contracts/proto/modulith/documents/v1).
//
//   - server.go: Server wraps any manager.Service.
//   - client.go: Client is a manager.Service over the wire, with the same
//     results and errors (callers can't tell which they have).
//   - grpc.go: conversions, and wireErrors, the one table mapping sentinels
//     to status codes and ErrorInfo reasons in both directions.
//
// UpdateDocument and RevertDocument are special: an edit applied but not published, or whose
// asset uses failed, still succeeds with its delta, the problem carried in
// the response, because the caller must get the delta either way.
package grpcapi
