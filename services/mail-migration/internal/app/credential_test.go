package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
)

func claimWithCredential(t *testing.T, f *fixture) *ClaimedJob {
	t.Helper()
	if _, err := f.uc.Create(context.Background(), f.tenant, f.actor, validInput()); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.uc.Claim(context.Background(), "runner-1")
	if err != nil || claimed == nil {
		t.Fatalf("reclamo: %+v %v", claimed, err)
	}
	return claimed
}

func withCredentials(c *Config) {
	c.JobCredentials = true
	c.MaxRunningJobs = 10
	c.MaxActivePerTenant = 10
}

func TestReclamarEntregaUnaCredencialDeDestinoQueSoloAbreEseTrabajo(t *testing.T) {
	f := newFixture(t, withCredentials)
	claimed := claimWithCredential(t, f)
	if !strings.HasPrefix(claimed.DestinationPassword, domain.DestinationTokenPrefix+".") {
		t.Fatalf("token: %q", claimed.DestinationPassword)
	}
	if strings.Contains(claimed.DestinationPassword, claimed.SourcePassword) {
		t.Fatal("el token no debe llevar la contrasena de origen")
	}
	v, err := f.uc.VerifyDestinationCredential(context.Background(), claimed.DestinationPassword, "ana@acme.test")
	if err != nil {
		t.Fatal(err)
	}
	if v.TenantID != f.tenant || v.JobID != claimed.JobID || v.Username != "ana@acme.test" {
		t.Fatalf("verificacion: %+v", v)
	}
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), claimed.DestinationPassword, "ANA@acme.test"); err != nil {
		t.Fatalf("el nombre no distingue mayusculas: %v", err)
	}
}

func TestLaBaseSoloGuardaElHashDeLaCredencial(t *testing.T) {
	f := newFixture(t, withCredentials)
	claimed := claimWithCredential(t, f)
	parsed, err := domain.ParseDestinationToken(claimed.DestinationPassword)
	if err != nil {
		t.Fatal(err)
	}
	stored := f.repo.Hashes[claimed.JobID]
	if len(stored) != 32 || !parsed.Matches(stored) {
		t.Fatalf("hash guardado: %x", stored)
	}
	secret := claimed.DestinationPassword[strings.LastIndex(claimed.DestinationPassword, ".")+1:]
	if strings.Contains(string(stored), secret) {
		t.Fatal("se guardo el secreto en claro")
	}
}

func TestLaCredencialNoAbreOtroBuzonNiOtroTrabajo(t *testing.T) {
	f := newFixture(t, withCredentials)
	claimed := claimWithCredential(t, f)

	if _, err := f.uc.VerifyDestinationCredential(context.Background(), claimed.DestinationPassword, "bea@acme.test"); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("otro buzon: %v", err)
	}

	other, err := domain.NewDestinationCredential(strings.NewReader(strings.Repeat("x", 64)))
	if err != nil {
		t.Fatal(err)
	}
	forged := other.Token(f.tenant, claimed.JobID)
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), forged, "ana@acme.test"); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("secreto ajeno: %v", err)
	}
	wrongJob := other.Token(f.tenant, uuid.New())
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), wrongJob, "ana@acme.test"); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("trabajo inexistente: %v", err)
	}
	// El token de un trabajo apuntado a otra empresa tampoco abre nada: la fila se busca por empresa.
	swapped := strings.Replace(claimed.DestinationPassword, f.tenant.String(), uuid.NewString(), 1)
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), swapped, "ana@acme.test"); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("otra empresa: %v", err)
	}
}

func TestLaCredencialDeUnTrabajoNoAbreElDeOtroDelMismoBuzon(t *testing.T) {
	f := newFixture(t, withCredentials)
	first := claimWithCredential(t, f)
	if _, err := f.uc.Complete(context.Background(), f.tenant, first.JobID, CompleteInput{LeaseID: first.LeaseID, Outcome: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	second := claimWithCredential(t, f)
	if first.DestinationPassword == second.DestinationPassword {
		t.Fatal("dos trabajos comparten credencial")
	}
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), first.DestinationPassword, "ana@acme.test"); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("el token del trabajo cerrado sigue abriendo: %v", err)
	}
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), second.DestinationPassword, "ana@acme.test"); err != nil {
		t.Fatalf("el token vigente deja de abrir: %v", err)
	}
}

func TestLaCredencialDejaDeAbrirAlCerrarElTrabajo(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed", "cancelled"} {
		f := newFixture(t, withCredentials)
		claimed := claimWithCredential(t, f)
		in := CompleteInput{LeaseID: claimed.LeaseID, Outcome: outcome}
		if outcome == "failed" {
			in.ErrorCode = "imapsync_failed"
		}
		if _, err := f.uc.Complete(context.Background(), f.tenant, claimed.JobID, in); err != nil {
			t.Fatalf("%s: %v", outcome, err)
		}
		if _, err := f.uc.VerifyDestinationCredential(context.Background(), claimed.DestinationPassword, "ana@acme.test"); !errors.Is(err, domain.ErrCredentialInvalid) {
			t.Fatalf("%s: la credencial sigue abriendo: %v", outcome, err)
		}
	}
}

func TestLaCredencialDejaDeAbrirAlPedirLaCancelacion(t *testing.T) {
	f := newFixture(t, withCredentials)
	claimed := claimWithCredential(t, f)
	if _, err := f.uc.Cancel(context.Background(), f.tenant, f.actor, claimed.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), claimed.DestinationPassword, "ana@acme.test"); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("cancelado pero sigue abriendo: %v", err)
	}
}

func TestLaCredencialCaducaConElLeaseYSeRenuevaConCadaLatido(t *testing.T) {
	f := newFixture(t, withCredentials)
	claimed := claimWithCredential(t, f)

	f.now = f.now.Add(60 * time.Second)
	if _, err := f.uc.Heartbeat(context.Background(), f.tenant, claimed.JobID, HeartbeatInput{LeaseID: claimed.LeaseID, Phase: "initial"}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(60 * time.Second)
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), claimed.DestinationPassword, "ana@acme.test"); err != nil {
		t.Fatalf("con latido al dia debe abrir: %v", err)
	}
	f.now = f.now.Add(91 * time.Second)
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), claimed.DestinationPassword, "ana@acme.test"); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("sin latido y con el lease vencido abre: %v", err)
	}
}

func TestUnNuevoReclamoInvalidaLaCredencialAnterior(t *testing.T) {
	f := newFixture(t, withCredentials)
	first := claimWithCredential(t, f)
	f.now = f.now.Add(2 * time.Minute)
	second, err := f.uc.Claim(context.Background(), "runner-2")
	if err != nil || second == nil {
		t.Fatalf("reclamo tras vencer el lease: %+v %v", second, err)
	}
	if second.JobID != first.JobID || second.Attempt != 2 {
		t.Fatalf("no es el mismo trabajo reintentado: %+v", second)
	}
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), first.DestinationPassword, "ana@acme.test"); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("la credencial del ejecutor que perdio el trabajo abre: %v", err)
	}
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), second.DestinationPassword, "ana@acme.test"); err != nil {
		t.Fatalf("la nueva no abre: %v", err)
	}
}

func TestBorrarElBuzonRevocaLaCredencial(t *testing.T) {
	f := newFixture(t, withCredentials)
	claimed := claimWithCredential(t, f)
	if _, err := f.uc.PurgeMailbox(context.Background(), f.tenant, f.repo.Jobs[claimed.JobID].MailboxID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), claimed.DestinationPassword, "ana@acme.test"); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("buzon borrado pero la credencial abre: %v", err)
	}
}

func TestSinCredencialesActivasElTokenNoSeEmiteNiSeAcepta(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.MaxRunningJobs = 10 })
	claimed := claimWithCredential(t, f)
	if claimed.DestinationPassword != "" || len(f.repo.Hashes) != 0 {
		t.Fatalf("emitio credencial con la funcion apagada: %q", claimed.DestinationPassword)
	}
	other, err := domain.NewDestinationCredential(strings.NewReader(strings.Repeat("y", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), other.Token(f.tenant, claimed.JobID), "ana@acme.test"); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("acepto una credencial con la funcion apagada: %v", err)
	}
}

func TestUnFalloDeInfraestructuraNoSeDisfrazaDeRechazo(t *testing.T) {
	f := newFixture(t, withCredentials)
	claimed := claimWithCredential(t, f)
	boom := errors.New("base caida")
	f.repo.TargetErr = boom
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), claimed.DestinationPassword, "ana@acme.test"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	f.repo.TargetErr = nil
	f.tenants.ForErr = domain.ErrTenantUnknown
	if _, err := f.uc.VerifyDestinationCredential(context.Background(), claimed.DestinationPassword, "ana@acme.test"); !errors.Is(err, domain.ErrCredentialInvalid) {
		t.Fatalf("empresa desconocida: %v", err)
	}
}

func TestFormatosDeTokenInvalidosSeRechazanSinTocarLaBase(t *testing.T) {
	f := newFixture(t, withCredentials)
	f.repo.TargetErr = errors.New("no debe consultarse la base")
	for _, token := range []string{
		"", "cfmj1", "cfmj1...", "otro." + uuid.NewString() + "." + uuid.NewString() + ".AAAA",
		"cfmj1." + strings.ToUpper(uuid.NewString()) + "." + uuid.NewString() + "." + strings.Repeat("A", 43),
		"cfmj1." + uuid.NewString() + "." + uuid.NewString() + "." + strings.Repeat("A", 42),
		"cfmj1." + uuid.NewString() + "." + uuid.NewString() + "." + strings.Repeat("A", 43) + ".extra",
		"cfmj1." + uuid.Nil.String() + "." + uuid.NewString() + "." + strings.Repeat("A", 43),
	} {
		if _, err := f.uc.VerifyDestinationCredential(context.Background(), token, "ana@acme.test"); !errors.Is(err, domain.ErrCredentialInvalid) {
			t.Errorf("%q: %v", token, err)
		}
	}
}
