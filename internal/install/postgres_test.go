package install

import (
	"encoding/json"
	"testing"
)

func TestFlexInt(t *testing.T) {
	var v []struct {
		M flexInt `json:"m"`
	}
	if err := json.Unmarshal([]byte(`[{"m":3},{"m":"21"}]`), &v); err != nil || v[0].M != 3 || v[1].M != 21 {
		t.Fatalf("got %v, %v", v, err)
	}
}
