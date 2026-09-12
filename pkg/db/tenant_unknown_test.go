package db

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestIsUnknownTenant(t *testing.T) {
	wrapped := fmt.Errorf("resolve tenant db for %s: %w", "00000000-0000-0000-0000-000000000001", pgx.ErrNoRows)
	if !IsUnknownTenant(wrapped) {
		t.Fatal("un tenant ausente del registro debe ser definitivo")
	}
	if IsUnknownTenant(fmt.Errorf("dial tcp: connection refused")) {
		t.Fatal("un fallo de red no puede tomarse como tenant inexistente")
	}
}
