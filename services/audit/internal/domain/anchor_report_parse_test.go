package domain

import (
	"strings"
	"testing"
	"time"
)

// El bloque firmado se lee campo a campo: el orden de los campos de una linea no importa (la
// firma cubre los bytes, no el parseo), pero cada campo esta una vez y no sobra ninguno.
func TestLosCamposDeUnaLineaSeLeenEnCualquierOrden(t *testing.T) {
	good := sampleReport().Render(nil)
	line := "anchor: tenant=" + tenantA.String() + " slug=- chain=audit_logs seq=5 hash=" + hashA + " hash_version=1 anchored_at=2026-09-21T09:46:00Z\n"
	reordered := "anchor: anchored_at=2026-09-21T09:46:00Z hash_version=1 hash=" + hashA + " seq=5 chain=audit_logs slug=- tenant=" + tenantA.String() + "\n"
	if !strings.Contains(good, line) {
		t.Fatal("la prueba no encuentra la linea que quiere reordenar")
	}
	p, err := ParseAnchorReport(strings.Replace(good, line, reordered, 1))
	if err != nil {
		t.Fatal(err)
	}
	if p.Anchors[0].HeadSeq != 5 || p.Anchors[0].HeadHash != hashA || !p.Anchors[0].AnchoredAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("%+v", p.Anchors[0])
	}
	// Pero el bloque leido es el reordenado: la firma ya no cuadraria, y eso es lo correcto.
	if !strings.Contains(string(p.Block), reordered) {
		t.Fatal("el bloque leido no conserva los bytes tal como llegaron")
	}
}

func TestUnaLineaDeRoturaMalFormadaSeRechaza(t *testing.T) {
	r := sampleReport()
	r.Cause, r.Broken = ReportCauseChainBroken, &ChainBreak{TenantID: tenantA, Chain: ChainAuditLogs, Reason: ReasonChainBroken}
	good := r.Render(nil)
	brokenLine := "broken: tenant=" + tenantA.String() + " chain=audit_logs reason=chain_broken\n"
	if !strings.Contains(good, brokenLine) {
		t.Fatalf("falta la linea de rotura:\n%s", good)
	}
	for name, bad := range map[string]string{
		"sin motivo":             "broken: tenant=" + tenantA.String() + " chain=audit_logs\n",
		"empresa que no es uuid": "broken: tenant=acme chain=audit_logs reason=chain_broken\n",
		"valor vacio":            "broken: tenant=" + tenantA.String() + " chain= reason=chain_broken\n",
		"sin separador":          "broken tenant=" + tenantA.String() + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAnchorReport(strings.Replace(good, brokenLine, bad, 1)); err == nil {
				t.Fatal("se acepto")
			}
		})
	}
}

func TestUnAnclaConCamposIlegiblesSeRechaza(t *testing.T) {
	good := sampleReport().Render(nil)
	for name, mut := range map[string][2]string{
		"seq no numerico":       {"seq=5 ", "seq=cinco "},
		"version no numerica":   {"hash_version=1 ", "hash_version=v1 "},
		"fecha no rfc3339":      {"anchored_at=2026-09-21T09:46:00Z", "anchored_at=ayer"},
		"tenant no uuid":        {"tenant=" + tenantA.String() + " slug=-", "tenant=acme slug=-"},
		"hash con letras":       {"hash=" + hashA, "hash=" + strings.Repeat("zz", 32)},
		"format no numerico":    {"format: 1", "format: uno"},
		"generated_at ilegible": {"generated_at: 2026-09-22T00:00:00Z", "generated_at: hoy"},
	} {
		t.Run(name, func(t *testing.T) {
			text := strings.Replace(good, mut[0], mut[1], 1)
			if text == good {
				t.Fatal("la mutacion no cambio nada")
			}
			if _, err := ParseAnchorReport(text); err == nil {
				t.Fatal("se acepto")
			}
		})
	}
}

func TestUnBloqueSinCabeceraSeRechaza(t *testing.T) {
	good := sampleReport().Render(nil)
	for name, drop := range map[string]string{
		"sin cause":        "cause: scheduled\n",
		"sin generated_at": "generated_at: 2026-09-22T00:00:00Z\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAnchorReport(strings.Replace(good, drop, "", 1)); err == nil {
				t.Fatal("se acepto")
			}
		})
	}
	// Sin format el valor queda en 0, que no es el formato conocido.
	if _, err := ParseAnchorReport(strings.Replace(good, "format: 1\n", "", 1)); err == nil {
		t.Fatal("se acepto un bloque sin formato")
	}
}

func TestElMarcadorDeFinAntesDelDeInicioSeRechaza(t *testing.T) {
	text := reportEnd + "\nformat: 1\n" + reportBegin + "\nsignature: none\n"
	if _, err := ParseAnchorReport(text); err == nil {
		t.Fatal("se acepto")
	}
}

func TestUnInformeVacioEsValidoYNoLlevaAnclas(t *testing.T) {
	r := AnchorReport{GeneratedAt: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC), Cause: ReportCauseScheduled}
	p, err := ParseAnchorReport(r.Render(nil))
	if err != nil || len(p.Anchors) != 0 || p.Cause != ReportCauseScheduled {
		t.Fatalf("%+v %v", p, err)
	}
	if string(r.Block()) != "format: 1\ngenerated_at: 2026-09-22T00:00:00Z\ncause: scheduled\n" {
		t.Fatalf("bloque vacio:\n%s", r.Block())
	}
}

func TestElCuerpoExplicaElMotivoYDondeVerificar(t *testing.T) {
	body := sampleReport().Render(&ReportSignature{KeyID: "0123456789abcdef", MAC: mac})
	for _, want := range []string{"Motivo: informe periodico", "ops/security/verificar-ancla.sh", "Guardelo fuera del servidor", "signature: hmac-sha256 key_id=0123456789abcdef mac=" + strings.Repeat("5a", 32)} {
		if !strings.Contains(body, want) {
			t.Fatalf("falta %q en:\n%s", want, body)
		}
	}
	r := sampleReport()
	r.Cause, r.Broken = ReportCauseChainBroken, &ChainBreak{TenantID: tenantB, Chain: ChainSecurityEvents, Reason: ReasonAnchorMismatch}
	if body := r.Render(nil); !strings.Contains(body, "Motivo: una verificacion dio por rota la cadena security_events de la empresa "+tenantB.String()+" (anchor_mismatch)") {
		t.Fatalf("motivo de rotura:\n%s", body)
	}
}

func TestLasHorasSeEscribenEnUTC(t *testing.T) {
	r := sampleReport()
	madrid := time.FixedZone("madrid", 2*3600)
	r.GeneratedAt = time.Date(2026, 9, 22, 2, 0, 0, 0, madrid)
	r.Tenants[1].Anchors[0].AnchoredAt = time.Date(2026, 9, 22, 1, 30, 0, 0, madrid)
	block := string(r.Block())
	if !strings.Contains(block, "generated_at: 2026-09-22T00:00:00Z\n") || !strings.Contains(block, "anchored_at=2026-09-21T23:30:00Z") {
		t.Fatalf("horas:\n%s", block)
	}
	if r.Subject() != "[Core Force Mail] Anclas de auditoria 2026-09-22" {
		t.Fatalf("asunto: %s", r.Subject())
	}
}
