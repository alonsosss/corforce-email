package response_test

import (
	"encoding/json"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/response"
)

func TestPageMetaCappedDeclaraElTope(t *testing.T) {
	raw, err := json.Marshal(response.PageMetaCapped(10000, true, 2, 50))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["total"] != float64(10000) || got["total_pages"] != float64(200) || got["total_capped"] != true {
		t.Fatalf("meta acotada: %s", raw)
	}
}

func TestPageMetaCappedExactoNoLlevaLaMarca(t *testing.T) {
	raw, err := json.Marshal(response.PageMetaCapped(137, false, 1, 20))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["total_capped"]; ok || got["total"] != float64(137) || got["total_pages"] != float64(7) {
		t.Fatalf("meta exacta: %s", raw)
	}
}
