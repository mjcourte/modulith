package telemetry

import (
	"fmt"
	"strconv"

	"github.com/mjcourte/modulith/platform/config"
)

// Settings is what telemetry reads from the environment.
type Settings struct {
	// Endpoint is the OTLP/gRPC collector, e.g. "http://localhost:4317".
	// Empty: no telemetry.
	Endpoint string
	// HotRate is the share of Hot roots kept, 0 to 1.
	HotRate Rate
	// DebugAddr serves pprof (StartDebug), e.g. "localhost:6060". Empty: off.
	DebugAddr string
}

// DefaultHotRate keeps one hot trace in a hundred.
const DefaultHotRate Rate = 0.01

// SettingsFromEnv reads MODULITH_OTLP_ENDPOINT, MODULITH_TRACE_HOT_RATE and
// MODULITH_DEBUG_ADDR. Problems are recorded in env; check env.Err() after
// reading everything.
func SettingsFromEnv(env *config.Env) Settings {
	s := Settings{
		Endpoint:  env.String("MODULITH_OTLP_ENDPOINT", ""),
		HotRate:   DefaultHotRate,
		DebugAddr: env.String("MODULITH_DEBUG_ADDR", ""),
	}
	env.Text("MODULITH_TRACE_HOT_RATE", &s.HotRate)
	return s
}

// Rate is a sampling share from 0 (none) to 1 (all).
type Rate float64

func (r *Rate) UnmarshalText(b []byte) error {
	v, err := strconv.ParseFloat(string(b), 64)
	if err != nil || v < 0 || v > 1 {
		return fmt.Errorf("want a number from 0 to 1, got %q", b)
	}
	*r = Rate(v)
	return nil
}
