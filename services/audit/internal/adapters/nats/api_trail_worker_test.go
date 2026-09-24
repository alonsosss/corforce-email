package nats

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestElApunteDelRastroTomaElIdDelEvento(t *testing.T) {
	eventID, tenant, user := uuid.New(), uuid.New(), uuid.New()
	data := map[string]interface{}{
		"user_id": user.String(), "method": "POST", "module": "billing", "path": "/api/v1/billing/invoices",
		"ip": "203.0.113.4", "user_agent": "curl/8", "request_id": "req-1", "roles": "tenant_admin", "status": float64(201),
	}
	l := trailLog(eventID.String(), tenant, data)
	if l.ID != eventID {
		t.Fatalf("id %v: una reentrega del bus debe chocar con el apunte ya guardado", l.ID)
	}
	if l.TenantID != tenant || l.UserID != user || l.Action != "POST" || l.Module != "billing" ||
		l.Resource != "/api/v1/billing/invoices" || l.IPAddress != "203.0.113.4" || l.Severity != "info" {
		t.Fatalf("apunte: %+v", l)
	}
	if l.UserAgent == nil || *l.UserAgent != "curl/8" || l.RequestID == nil || *l.RequestID != "req-1" || l.Changes == nil {
		t.Fatalf("apunte: %+v", l)
	}
}

func TestElApunteDelRastroSinIdDeEventoLoDejaParaLaBitacora(t *testing.T) {
	for _, id := range []string{"", "no-es-uuid"} {
		if l := trailLog(id, uuid.New(), map[string]interface{}{}); l.ID != uuid.Nil {
			t.Fatalf("id %q produjo %v", id, l.ID)
		}
	}
}

func TestLaSeveridadYLaIPDelRastroSalenDelResultado(t *testing.T) {
	casos := []struct {
		status   interface{}
		severity string
	}{
		{float64(200), "info"}, {float64(399), "info"}, {float64(400), "warning"},
		{float64(403), "warning"}, {float64(500), "warning"}, {nil, "info"}, {"500", "info"},
	}
	for _, c := range casos {
		l := trailLog("", uuid.New(), map[string]interface{}{"status": c.status})
		if l.Severity != c.severity {
			t.Errorf("status %v: %s, se esperaba %s", c.status, l.Severity, c.severity)
		}
		if l.IPAddress != "0.0.0.0" || l.UserAgent != nil || l.RequestID != nil {
			t.Errorf("sin ip, agente ni id de peticion: %+v", l)
		}
	}
}

// request_id, action y module son varchar(100): un valor mas largo hace fallar el INSERT, el apunte
// no se guarda nunca (el bus lo reentrega hasta abandonarlo) y la escritura queda sin rastro. El
// identificador lo elige el cliente, asi que el apunte se recorta en lugar de perderse.
func TestElApunteDelRastroNoSuperaLasColumnasDeLaBitacora(t *testing.T) {
	largo := strings.Repeat("x", 500)
	l := trailLog("", uuid.New(), map[string]interface{}{
		"request_id": largo, "module": largo, "method": largo, "path": "/api/v1/x",
	})
	if l.RequestID == nil || len(*l.RequestID) != maxShortColumn || len(l.Module) != maxShortColumn || len(l.Action) != maxShortColumn {
		t.Fatalf("request_id %v, module %d, action %d", l.RequestID, len(l.Module), len(l.Action))
	}
	if l.Resource != "/api/v1/x" {
		t.Fatalf("un valor corto no se toca: %q", l.Resource)
	}
	if got := truncateRunes("ñ"+largo, 3); got != "ñxx" {
		t.Fatalf("el recorte no parte un caracter: %q", got)
	}
}

func TestUnUsuarioRotoQuedaSinIdentidad(t *testing.T) {
	l := trailLog("", uuid.New(), map[string]interface{}{"user_id": "roto"})
	if l.UserID != uuid.Nil {
		t.Fatalf("usuario %v", l.UserID)
	}
}

func TestTrailStrNoFallaConTiposAjenos(t *testing.T) {
	for _, v := range []interface{}{nil, 5, true, []string{"a"}, map[string]interface{}{}} {
		if got := trailStr(v); got != "" {
			t.Errorf("%v -> %q", v, got)
		}
	}
	if trailStr("x") != "x" {
		t.Fatal("una cadena pasa tal cual")
	}
}

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

// Una escritura con clave de API no tiene usuario: el apunte guarda la clave que la hizo.
func TestElRastroGuardaLaClaveDeAPI(t *testing.T) {
	var detail map[string]interface{}
	got := trailChanges(map[string]interface{}{"roles": "", "status": float64(202), "api_key_id": "5d8c7a1e-0b7e-4a52-9c3e-2f7b1c9d0e11"})
	if got == nil || json.Unmarshal([]byte(*got), &detail) != nil {
		t.Fatalf("detalle ilegible: %v", got)
	}
	if detail["api_key_id"] != "5d8c7a1e-0b7e-4a52-9c3e-2f7b1c9d0e11" || len(detail) != 3 {
		t.Fatalf("detalle con clave de API: %v", detail)
	}
}
