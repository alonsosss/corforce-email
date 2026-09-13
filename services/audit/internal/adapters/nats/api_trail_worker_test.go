package nats

import (
	"encoding/json"
	"testing"
)

// El apunte de una peticion con celda destino guarda la celda pedida; el de una escritura sin
// ella queda como antes.
func TestElRastroGuardaLaCeldaDestino(t *testing.T) {
	var detail map[string]interface{}
	got := trailChanges(map[string]interface{}{"roles": "superadmin", "status": float64(200), "target_cell": "pe-02", "path": "/api/v1/x"})
	if got == nil || json.Unmarshal([]byte(*got), &detail) != nil {
		t.Fatalf("detalle ilegible: %v", got)
	}
	if detail["target_cell"] != "pe-02" || detail["roles"] != "superadmin" || detail["status"] != float64(200) || len(detail) != 3 {
		t.Fatalf("detalle con celda destino: %v", detail)
	}

	detail = nil
	got = trailChanges(map[string]interface{}{"roles": "tenant_admin", "status": float64(201)})
	if got == nil || json.Unmarshal([]byte(*got), &detail) != nil {
		t.Fatalf("detalle ilegible: %v", got)
	}
	if _, ok := detail["target_cell"]; ok || len(detail) != 2 {
		t.Fatalf("detalle sin celda destino: %v", detail)
	}
}
