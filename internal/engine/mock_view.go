package engine

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strconv"

	"github.com/mjcourte/modulith/internal/engine/fbs"
)

// viewJSON is a View's envelope. Field order here is the output order, and
// matches the sorted-keys rule in View's doc comment.
type viewJSON struct {
	Clock     string               `json:"clock"`
	Registers map[string]viewValue `json:"registers"`
}

// viewValue writes one register's value per View's contract: i64 as decimal
// strings, bytes as b64, AssetRef as obj.
type viewValue struct{ v *fbs.ValueT }

func (x viewValue) MarshalJSON() ([]byte, error) {
	switch t := x.v.Value.(type) {
	case *fbs.IntValT:
		return encodeJSON(strconv.FormatInt(t.V, 10))
	case *fbs.FloatValT:
		return encodeJSON(t.V) // Go's float already is ES6 Number.toString
	case *fbs.StrValT:
		return encodeJSON(t.V)
	case *fbs.BytesValT:
		return encodeJSON(base64.StdEncoding.EncodeToString(t.V))
	case *fbs.AssetRefT:
		return encodeJSON(struct {
			AssetID string `json:"assetId"`
			Version int32  `json:"version"`
		}{t.AssetId, t.Version})
	}
	return []byte("null"), nil // valueNONE
}

// encodeJSON marshals v with HTML escaping off. encoding/json escapes <, > and
// & by default and serde_json does not, so leaving it on would make the two
// implementations disagree on any string holding them. Every encoding in this
// file goes through here, including the nested ones: json.Marshal would
// re-escape what MarshalJSON returns.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil // Encode appends one
}
