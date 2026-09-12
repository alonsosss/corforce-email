package app

import (
	"context"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// fakeLogs simula el historial de la bitacora sobre el que trabaja el detector.
type fakeLogs struct {
	knownIP     bool
	agents      []string
	recentFails int64
}

func (f *fakeLogs) Create(context.Context, *domain.AuditLog) error { return nil }
func (f *fakeLogs) VerifyChain(context.Context, uuid.UUID) (*domain.ChainIntegrity, error) {
	return &domain.ChainIntegrity{OK: true}, nil
}
func (f *fakeLogs) RecentLoginOtherIP(context.Context, uuid.UUID, string, time.Time, uuid.UUID) (string, error) {
	return "", nil
}
func (f *fakeLogs) GetByID(context.Context, uuid.UUID, uuid.UUID) (*domain.AuditLog, error) {
	return nil, nil
}
func (f *fakeLogs) List(context.Context, domain.AuditQuery, int, int) ([]*domain.AuditLog, error) {
	return nil, nil
}
func (f *fakeLogs) Count(context.Context, domain.AuditQuery) (int64, error) { return 0, nil }
func (f *fakeLogs) BulkCreate(context.Context, []*domain.AuditLog) error    { return nil }
func (f *fakeLogs) HasUserActionFromIP(context.Context, uuid.UUID, string, string, uuid.UUID) (bool, error) {
	return f.knownIP, nil
}
func (f *fakeLogs) ListUserActionAgents(context.Context, uuid.UUID, string, uuid.UUID, int) ([]string, error) {
	return f.agents, nil
}
func (f *fakeLogs) CountRecentByActionIP(context.Context, string, string, time.Time) (int64, error) {
	return f.recentFails, nil
}

// fakeSecurity captura los eventos levantados por el detector.
type fakeSecurity struct {
	created    []*domain.SecurityEvent
	duplicated bool
}

func (f *fakeSecurity) Create(_ context.Context, e *domain.SecurityEvent) error {
	f.created = append(f.created, e)
	return nil
}
func (f *fakeSecurity) GetByID(context.Context, uuid.UUID, uuid.UUID) (*domain.SecurityEvent, error) {
	return nil, nil
}
func (f *fakeSecurity) List(context.Context, uuid.UUID, ports.SecurityFilters, int, int) ([]*domain.SecurityEvent, int64, error) {
	return nil, 0, nil
}
func (f *fakeSecurity) Acknowledge(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (f *fakeSecurity) GetUnacknowledged(context.Context, uuid.UUID) ([]*domain.SecurityEvent, error) {
	return nil, nil
}
func (f *fakeSecurity) HasRecentEvent(context.Context, uuid.UUID, string, string, time.Time) (bool, error) {
	return f.duplicated, nil
}

type fakePublisher struct{ alerts int }

func (f *fakePublisher) PublishSecurityAlert(_, _, _, _, _, _ string) error {
	f.alerts++
	return nil
}

func newDetector(logs *fakeLogs, sec *fakeSecurity) (*SecurityDetector, *fakePublisher) {
	pub := &fakePublisher{}
	return NewSecurityDetector(logs, sec, pub, SecurityDetectorConfig{
		BruteForceMax:    5,
		BruteForceWindow: 15 * time.Minute,
	}, zap.NewNop()), pub
}

func loginLog(ip string) *domain.AuditLog {
	return &domain.AuditLog{
		ID:        uuid.New(),
		TenantID:  uuid.New(),
		UserID:    uuid.New(),
		Action:    "user.logged_in",
		IPAddress: ip,
	}
}

func types(events []*domain.SecurityEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.EventType)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

const chromeLinux = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"

func TestLoginDesdeIPNuevaLevantaAlerta(t *testing.T) {
	logs := &fakeLogs{knownIP: false, agents: []string{chromeLinux}}
	sec := &fakeSecurity{}
	d, pub := newDetector(logs, sec)

	d.Inspect(context.Background(), loginLog("200.10.1.5"), chromeLinux)

	if !contains(types(sec.created), "login_new_ip") {
		t.Fatalf("esperaba login_new_ip, hubo: %v", types(sec.created))
	}
	if pub.alerts == 0 {
		t.Fatal("la alerta debe publicarse para que el aviso pueda salir")
	}
}

func TestLoginDesdeIPConocidaNoAlerta(t *testing.T) {
	logs := &fakeLogs{knownIP: true, agents: []string{chromeLinux}}
	sec := &fakeSecurity{}
	d, _ := newDetector(logs, sec)

	d.Inspect(context.Background(), loginLog("200.10.1.5"), chromeLinux)

	if contains(types(sec.created), "login_new_ip") {
		t.Fatal("una IP ya vista no puede levantar alerta")
	}
}

// El primer login de un usuario no tiene historial contra el que comparar: alertar ahi
// convertiria cada alta en un incidente.
func TestPrimerLoginSinHistorialNoAlerta(t *testing.T) {
	logs := &fakeLogs{knownIP: false, agents: nil}
	sec := &fakeSecurity{}
	d, _ := newDetector(logs, sec)

	d.Inspect(context.Background(), loginLog("200.10.1.5"), chromeLinux)

	if len(sec.created) != 0 {
		t.Fatalf("sin historial no debe haber alertas, hubo: %v", types(sec.created))
	}
}

func TestDispositivoNuevoLevantaAlerta(t *testing.T) {
	logs := &fakeLogs{knownIP: true, agents: []string{chromeLinux}}
	sec := &fakeSecurity{}
	d, _ := newDetector(logs, sec)

	iphone := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
	d.Inspect(context.Background(), loginLog("200.10.1.5"), iphone)

	if !contains(types(sec.created), "login_new_device") {
		t.Fatalf("esperaba login_new_device, hubo: %v", types(sec.created))
	}
}

// Comparar el user-agent crudo dispararia una alerta con cada actualizacion menor del
// navegador; la comparacion es por familia navegador+SO.
func TestMismaFamiliaDeDispositivoNoAlerta(t *testing.T) {
	logs := &fakeLogs{knownIP: true, agents: []string{chromeLinux}}
	sec := &fakeSecurity{}
	d, _ := newDetector(logs, sec)

	chromeNuevo := "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.7000.1 Safari/537.36"
	d.Inspect(context.Background(), loginLog("200.10.1.5"), chromeNuevo)

	if contains(types(sec.created), "login_new_device") {
		t.Fatal("una version nueva del mismo navegador no es un dispositivo nuevo")
	}
}

// Sin IP real (el marcador de ausencia) no hay heuristica de red que valga: alertar
// agrupando a todos bajo 0.0.0.0 seria ruido puro.
func TestSinIPRealNoAlerta(t *testing.T) {
	logs := &fakeLogs{knownIP: false, agents: []string{chromeLinux}}
	sec := &fakeSecurity{}
	d, _ := newDetector(logs, sec)

	d.Inspect(context.Background(), loginLog("0.0.0.0"), chromeLinux)

	if len(sec.created) != 0 {
		t.Fatalf("sin IP real no debe haber alertas, hubo: %v", types(sec.created))
	}
}

func TestFuerzaBrutaAlSuperarElUmbral(t *testing.T) {
	logs := &fakeLogs{recentFails: 5}
	sec := &fakeSecurity{}
	d, _ := newDetector(logs, sec)

	l := loginLog("200.10.1.5")
	l.Action = "user.login_failed"
	d.Inspect(context.Background(), l, "")

	if !contains(types(sec.created), "brute_force") {
		t.Fatalf("5 fallos alcanzan el umbral, hubo: %v", types(sec.created))
	}
	if sec.created[0].RiskLevel != "high" {
		t.Fatalf("la fuerza bruta es riesgo alto, fue %q", sec.created[0].RiskLevel)
	}
}

func TestFuerzaBrutaBajoElUmbralNoAlerta(t *testing.T) {
	logs := &fakeLogs{recentFails: 4}
	sec := &fakeSecurity{}
	d, _ := newDetector(logs, sec)

	l := loginLog("200.10.1.5")
	l.Action = "user.login_failed"
	d.Inspect(context.Background(), l, "")

	if len(sec.created) != 0 {
		t.Fatal("por debajo del umbral no hay ataque que reportar")
	}
}

// Una rafaga genera UN evento por ventana, no uno por intento: si no, el aviso al
// administrador se vuelve inutilizable justo cuando mas importa.
func TestFuerzaBrutaNoSeDuplicaEnLaVentana(t *testing.T) {
	logs := &fakeLogs{recentFails: 20}
	sec := &fakeSecurity{duplicated: true}
	d, _ := newDetector(logs, sec)

	l := loginLog("200.10.1.5")
	l.Action = "user.login_failed"
	d.Inspect(context.Background(), l, "")

	if len(sec.created) != 0 {
		t.Fatal("ya habia una alerta viva para esta IP en la ventana")
	}
}

func TestCuentaBloqueadaYRevocacionSeRegistran(t *testing.T) {
	for _, caso := range []struct{ accion, tipo, riesgo string }{
		{"user.locked", "account_locked", "high"},
		{"session.revoked_by_admin", "session_revoked_by_admin", "low"},
	} {
		sec := &fakeSecurity{}
		d, _ := newDetector(&fakeLogs{}, sec)

		l := loginLog("200.10.1.5")
		l.Action = caso.accion
		d.Inspect(context.Background(), l, chromeLinux)

		if !contains(types(sec.created), caso.tipo) {
			t.Fatalf("%s debe registrar %s, hubo: %v", caso.accion, caso.tipo, types(sec.created))
		}
		if sec.created[0].RiskLevel != caso.riesgo {
			t.Fatalf("%s: riesgo esperado %q, fue %q", caso.accion, caso.riesgo, sec.created[0].RiskLevel)
		}
	}
}

func TestFamiliaDeDispositivo(t *testing.T) {
	casos := map[string]string{
		chromeLinux: "Chrome en Linux",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1": "Safari en iPhone",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/150.0.0.0 Safari/537.36 Edg/150.0.0.0":           "Edge en Windows",
		"curl/8.5.0": "curl en desconocido",
	}
	for ua, esperado := range casos {
		if got := deviceFamily(ua); got != esperado {
			t.Errorf("deviceFamily(%.40s...) = %q, esperaba %q", ua, got, esperado)
		}
	}
}
