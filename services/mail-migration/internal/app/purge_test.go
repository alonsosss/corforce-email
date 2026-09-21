package app

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
	"github.com/google/uuid"
)

func (f *fixture) seed(tenant, mailbox uuid.UUID, status domain.Status) *domain.Job {
	j := &domain.Job{
		ID: uuid.New(), TenantID: tenant, MailboxID: mailbox, MailboxUsername: "ana@acme.test",
		SourceHost: "imap.origen.example", SourcePort: 993, SourceTLS: domain.TLSImplicit, SourceUsername: "ana@origen.example",
		Status: status, Progress: emptyProgress(), CreatedAt: f.now,
	}
	if status.Active() {
		j.SourcePasswordEnc = []byte("enc:clave")
	} else {
		j.FinishedAt = &f.now
	}
	f.repo.Jobs[j.ID] = j
	return j
}

func TestPurgeBorraLosTrabajosDelBuzonEnCualquierEstadoYAnunciaLosActivos(t *testing.T) {
	f := newFixture(t, nil)
	mailbox := uuid.New()
	pending := f.seed(f.tenant, mailbox, domain.StatusPending)
	running := f.seed(f.tenant, mailbox, domain.StatusRunning)
	done := f.seed(f.tenant, mailbox, domain.StatusSucceeded)
	failed := f.seed(f.tenant, mailbox, domain.StatusFailed)

	removed, err := f.uc.PurgeMailbox(context.Background(), f.tenant, mailbox)
	if err != nil || removed != 4 {
		t.Fatalf("PurgeMailbox: %d %v", removed, err)
	}
	for _, j := range []*domain.Job{pending, running, done, failed} {
		if _, ok := f.repo.Jobs[j.ID]; ok {
			t.Errorf("el trabajo %s (%s) sigue en la base", j.ID, j.Status)
		}
	}
	got := append([]string(nil), f.events.Log...)
	sort.Strings(got)
	if want := []string{"finished:cancelled", "finished:cancelled"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("solo los activos anuncian su cancelacion: %v", f.events.Log)
	}
}

func TestPurgeNoTocaOtroBuzonDeLaMismaEmpresaNiOtraEmpresa(t *testing.T) {
	f := newFixture(t, nil)
	mailbox, sibling := uuid.New(), uuid.New()
	otherTenant := uuid.New()
	f.seed(f.tenant, mailbox, domain.StatusPending)
	keptSibling := f.seed(f.tenant, sibling, domain.StatusPending)
	// El mismo id de buzon en otra empresa: el borrado se acota por empresa.
	keptOther := f.seed(otherTenant, mailbox, domain.StatusRunning)

	if removed, err := f.uc.PurgeMailbox(context.Background(), f.tenant, mailbox); err != nil || removed != 1 {
		t.Fatalf("PurgeMailbox: %d %v", removed, err)
	}
	if _, ok := f.repo.Jobs[keptSibling.ID]; !ok {
		t.Error("se borro el trabajo de otro buzon de la misma empresa")
	}
	if got, ok := f.repo.Jobs[keptOther.ID]; !ok || got.Status != domain.StatusRunning || got.SourcePasswordEnc == nil {
		t.Errorf("se toco el trabajo de otra empresa: %+v", got)
	}
}

// Un buzon recreado con el mismo nombre tiene otro id: el evento del anterior no se lleva sus trabajos.
func TestPurgeIdentificaPorIdNoPorNombre(t *testing.T) {
	f := newFixture(t, nil)
	deleted, recreated := uuid.New(), uuid.New()
	old := f.seed(f.tenant, deleted, domain.StatusSucceeded)
	fresh := f.seed(f.tenant, recreated, domain.StatusPending)
	if old.MailboxUsername != fresh.MailboxUsername {
		t.Fatal("la prueba necesita el mismo nombre en los dos buzones")
	}

	if removed, err := f.uc.PurgeMailbox(context.Background(), f.tenant, deleted); err != nil || removed != 1 {
		t.Fatalf("PurgeMailbox: %d %v", removed, err)
	}
	if got, ok := f.repo.Jobs[fresh.ID]; !ok || got.Status != domain.StatusPending || got.SourcePasswordEnc == nil {
		t.Fatalf("el buzon recreado perdio su trabajo: %+v", got)
	}
}

func TestPurgeEsIdempotenteYUnBuzonSinTrabajosNoEsUnError(t *testing.T) {
	f := newFixture(t, nil)
	mailbox := uuid.New()
	f.seed(f.tenant, mailbox, domain.StatusRunning)

	for i, want := range []int{1, 0, 0} {
		removed, err := f.uc.PurgeMailbox(context.Background(), f.tenant, mailbox)
		if err != nil || removed != want {
			t.Fatalf("llamada %d: %d %v", i+1, removed, err)
		}
	}
	if len(f.events.Log) != 1 {
		t.Fatalf("repetir el evento no anuncia otra vez: %v", f.events.Log)
	}
	if removed, err := f.uc.PurgeMailbox(context.Background(), f.tenant, uuid.New()); err != nil || removed != 0 {
		t.Fatalf("buzon inexistente: %d %v", removed, err)
	}
}

func TestPurgeRechazaIdentificadoresNulosSinTocarNada(t *testing.T) {
	f := newFixture(t, nil)
	kept := f.seed(f.tenant, uuid.New(), domain.StatusPending)
	for _, c := range []struct{ tenant, mailbox uuid.UUID }{{uuid.Nil, uuid.New()}, {f.tenant, uuid.Nil}} {
		if _, err := f.uc.PurgeMailbox(context.Background(), c.tenant, c.mailbox); !errors.Is(err, domain.ErrInvalidMailbox) {
			t.Errorf("%v: %v", c, err)
		}
	}
	if _, ok := f.repo.Jobs[kept.ID]; !ok || len(f.events.Log) != 0 {
		t.Fatal("un identificador nulo no debe tocar nada")
	}
}

func TestPurgePropagaEmpresaDesconocidaYFalloDelEvento(t *testing.T) {
	f := newFixture(t, nil)
	mailbox := uuid.New()
	f.seed(f.tenant, mailbox, domain.StatusPending)

	f.tenants.ForErr = domain.ErrTenantUnknown
	if _, err := f.uc.PurgeMailbox(context.Background(), f.tenant, mailbox); !errors.Is(err, domain.ErrTenantUnknown) {
		t.Fatalf("empresa desconocida: %v", err)
	}
	f.tenants.ForErr = nil

	f.events.Fail = errors.New("outbox caida")
	if removed, err := f.uc.PurgeMailbox(context.Background(), f.tenant, mailbox); err == nil || removed != 0 {
		t.Fatalf("un fallo del evento no se traga: %d %v", removed, err)
	}
}
