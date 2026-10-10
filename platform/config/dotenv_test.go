package config

// An internal test (package config, not config_test): parseDotEnv is
// unexported, so only tests inside the package can call it.

import (
	"maps"
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	input := `
# local settings
GYOZA_DATABASE_URL=postgres://gyoza:gyoza@localhost:5432/gyoza
  GYOZA_LOG_LEVEL = debug
QUOTED="has spaces"
SINGLE='x=y'
EMPTY=
`
	got, err := parseDotEnv(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseDotEnv: %v", err)
	}
	want := map[string]string{
		"GYOZA_DATABASE_URL": "postgres://gyoza:gyoza@localhost:5432/gyoza",
		"GYOZA_LOG_LEVEL":    "debug",
		"QUOTED":             "has spaces",
		"SINGLE":             "x=y",
		"EMPTY":              "",
	}
	if !maps.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestParseDotEnvRejectsMalformedLines(t *testing.T) {
	for _, input := range []string{"NO_EQUALS_SIGN", "=value"} {
		if _, err := parseDotEnv(strings.NewReader(input)); err == nil {
			t.Errorf("parseDotEnv(%q) succeeded, want error", input)
		}
	}
}
