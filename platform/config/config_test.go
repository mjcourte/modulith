package config_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"gyoza/platform/config"
)

// mapEnv is a lookup backed by a map, standing in for the environment.
func mapEnv(vars map[string]string) *config.Env {
	return config.New(func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	})
}

func TestReadsValuesAndDefaults(t *testing.T) {
	env := mapEnv(map[string]string{
		"DB":    "postgres://x",
		"LEVEL": "debug",
		"EMPTY": "",
	})

	if got := env.Required("DB"); got != "postgres://x" {
		t.Errorf("Required(DB) = %q", got)
	}
	if got := env.String("MISSING", "fallback"); got != "fallback" {
		t.Errorf("String(MISSING) = %q, want fallback", got)
	}
	if got := env.String("EMPTY", "fallback"); got != "fallback" {
		t.Errorf("String(EMPTY) = %q, want empty treated as unset", got)
	}

	level := slog.LevelInfo
	env.Text("LEVEL", &level)
	if level != slog.LevelDebug {
		t.Errorf("Text(LEVEL) = %v, want debug", level)
	}
	unset := slog.LevelWarn
	env.Text("MISSING", &unset)
	if unset != slog.LevelWarn {
		t.Errorf("Text(MISSING) changed the default to %v", unset)
	}

	if err := env.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

func TestDuration(t *testing.T) {
	tests := []struct {
		value   string
		want    time.Duration
		wantErr bool
	}{
		{"", 5 * time.Second, false}, // unset: default
		{"1m30s", 90 * time.Second, false},
		{"soon", 5 * time.Second, true},
		{"0s", 5 * time.Second, true},
		{"-1s", 5 * time.Second, true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			env := mapEnv(map[string]string{"TIMEOUT": tt.value})
			if got := env.Duration("TIMEOUT", 5*time.Second); got != tt.want {
				t.Errorf("Duration = %v, want %v", got, tt.want)
			}
			if gotErr := env.Err() != nil; gotErr != tt.wantErr {
				t.Errorf("Err() = %v, wantErr %v", env.Err(), tt.wantErr)
			}
		})
	}
}

func TestReportsEveryProblemAtOnce(t *testing.T) {
	env := mapEnv(map[string]string{"LEVEL": "loud", "EMPTY": ""})
	env.Required("DB")
	env.Required("EMPTY")
	level := slog.LevelInfo
	env.Text("LEVEL", &level)

	err := env.Err()
	if err == nil {
		t.Fatal("Err() = nil, want errors")
	}
	for _, want := range []string{"DB is required", "EMPTY is required", "LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	// errors.Join keeps each problem reachable, not just the text.
	var joined interface{ Unwrap() []error }
	if !errors.As(err, &joined) || len(joined.Unwrap()) != 3 {
		t.Errorf("want 3 joined errors, got %v", err)
	}
}

func TestBool(t *testing.T) {
	tests := []struct {
		value   string
		want    bool
		wantErr bool
	}{
		{"", true, false}, // unset: default
		{"false", false, false},
		{"0", false, false},
		{"TRUE", true, false},
		{"yes", true, true}, // not a ParseBool form: error, default kept
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			env := mapEnv(map[string]string{"FLAG": tt.value})
			if got := env.Bool("FLAG", true); got != tt.want {
				t.Errorf("Bool = %v, want %v", got, tt.want)
			}
			if gotErr := env.Err() != nil; gotErr != tt.wantErr {
				t.Errorf("Err() = %v, wantErr %v", env.Err(), tt.wantErr)
			}
		})
	}
}
