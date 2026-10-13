// Package config reads settings from environment variables.
//
// Problems are collected rather than returned one at a time, so a
// misconfigured binary reports every missing or invalid setting at once:
//
//	env := config.FromEnv()
//	dbURL := env.Required("GYOZA_DATABASE_URL")
//	level := slog.LevelInfo
//	env.Text("GYOZA_LOG_LEVEL", &level)
//	if err := env.Err(); err != nil {
//		return err
//	}
package config

import (
	"encoding"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Env looks up settings and records any problems found along the way.
type Env struct {
	lookup func(name string) (string, bool)
	errs   []error
}

// New reads settings through lookup. Tests pass a map-backed lookup;
// binaries use FromEnv.
func New(lookup func(name string) (string, bool)) *Env {
	return &Env{lookup: lookup}
}

// FromEnv reads the process environment, falling back to a .env file in the
// working directory for local development. Real environment variables always
// win over the file. A missing .env file is fine; a malformed one is an error.
func FromEnv() *Env {
	file, err := readDotEnv(".env")
	e := New(func(name string) (string, bool) {
		if v, ok := os.LookupEnv(name); ok {
			return v, true
		}
		v, ok := file[name]
		return v, ok
	})
	if err != nil {
		e.errs = append(e.errs, err)
	}
	return e
}

// get returns the value of name, treating an empty value as unset.
func (e *Env) get(name string) (string, bool) {
	v, ok := e.lookup(name)
	return v, ok && v != ""
}

// Required returns the value of name, recording an error if it is unset.
func (e *Env) Required(name string) string {
	v, ok := e.get(name)
	if !ok {
		e.errs = append(e.errs, fmt.Errorf("%s is required", name))
	}
	return v
}

// String returns the value of name, or def if it is unset.
func (e *Env) String(name, def string) string {
	if v, ok := e.get(name); ok {
		return v
	}
	return def
}

// Text parses the value of name into dst, which keeps its current value
// (the default) if name is unset. Any type that can parse itself from text
// works, such as slog.Level or netip.Addr.
func (e *Env) Text(name string, dst encoding.TextUnmarshaler) {
	v, ok := e.get(name)
	if !ok {
		return
	}
	if err := dst.UnmarshalText([]byte(v)); err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", name, err))
	}
}

// Duration returns the value of name parsed as a positive duration such as
// "20s" or "1m30s", or def if it is unset.
func (e *Env) Duration(name string, def time.Duration) time.Duration {
	v, ok := e.get(name)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", name, err))
		return def
	}
	if d <= 0 {
		e.errs = append(e.errs, fmt.Errorf("%s: must be positive, got %s", name, v))
		return def
	}
	return d
}

// PositiveInt returns the value of name parsed as a positive whole number,
// or def if it is unset.
func (e *Env) PositiveInt(name string, def int64) int64 {
	v, ok := e.get(name)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		e.errs = append(e.errs, fmt.Errorf("%s: want a positive whole number, got %q", name, v))
		return def
	}
	return n
}

// Bool returns the value of name parsed as a boolean ("true", "false", "1",
// "0" and the other forms strconv.ParseBool accepts), or def if it is unset.
func (e *Env) Bool(name string, def bool) bool {
	v, ok := e.get(name)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: want true or false, got %q", name, v))
		return def
	}
	return b
}

// Err returns every problem recorded so far, or nil.
func (e *Env) Err() error {
	if len(e.errs) == 0 {
		return nil
	}
	return fmt.Errorf("config: %w", errors.Join(e.errs...))
}
