package snapshots_test

import (
	"errors"
	"testing"

	"gyoza/internal/engine"
	"gyoza/services/documents/internal/snapshots"
)

func TestContentTypeRoundTrip(t *testing.T) {
	for _, f := range []engine.WireFormat{engine.WireNative, engine.WireJSON} {
		ct, err := snapshots.ContentType(f, 3)
		if err != nil {
			t.Fatal(err)
		}
		gotF, gotV, err := snapshots.ParseContentType(ct)
		if err != nil || gotF != f || gotV != 3 {
			t.Errorf("%s: parsed %q as %s v%d, %v", f, ct, gotF, gotV, err)
		}
	}
	for _, bad := range []string{"image/png", "application/vnd.gyoza.app+flatbuffers", "application/json; version=x"} {
		if _, _, err := snapshots.ParseContentType(bad); !errors.Is(err, snapshots.ErrInvalid) {
			t.Errorf("%q: got %v, want ErrInvalid", bad, err)
		}
	}
}
