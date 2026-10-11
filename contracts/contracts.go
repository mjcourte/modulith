// Package contracts holds the protobuf API definitions (proto/) and the Go
// code generated from them (gen/go). This file only exists so that
// `go generate ./contracts` regenerates that code.
package contracts

//go:generate go tool buf lint
//go:generate go tool buf generate
