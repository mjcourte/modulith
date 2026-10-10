package postgres_test

import (
	"os"
	"testing"

	"gyoza/platform/postgres"
)

func TestConnectAppliesSessionSettings(t *testing.T) {
	url := os.Getenv("GYOZA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set GYOZA_TEST_DATABASE_URL to run against Postgres")
	}
	pool, err := postgres.Connect(t.Context(), url, "platform-test")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()

	for setting, want := range map[string]string{
		"plan_cache_mode":  "force_custom_plan",
		"application_name": "platform-test",
	} {
		var got string
		if err := pool.QueryRow(t.Context(), "SELECT current_setting($1)", setting).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", setting, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", setting, got, want)
		}
	}
}

func TestConnectFailsFastOnBadURL(t *testing.T) {
	// Nothing listens on port 1, so the startup ping must fail.
	_, err := postgres.Connect(t.Context(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1", "platform-test")
	if err == nil {
		t.Fatal("Connect succeeded against a closed port")
	}
}
