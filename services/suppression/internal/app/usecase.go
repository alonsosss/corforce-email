package app

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/suppression/internal/domain"
	"github.com/alonsosss/corforce-email/services/suppression/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// MaxCheckEmails es el tope de direcciones por consulta previa al envio.
	MaxCheckEmails = 1000
	// MaxImportEmails es el tope de direcciones por carga masiva.
	MaxImportEmails = 10000
	// SourceAPI marca lo que registra un operador por el API publico.
	SourceAPI = "api"
	// SourceImport marca lo que entra por carga masiva.
	SourceImport = "import"
)

type Deps struct {
	Entries ports.EntryRepository
	Imports ports.ImportRepository
	Tx      ports.Transactor
	Events  ports.EventPublisher
	Logger  *zap.Logger
	// Now permite fijar el reloj en las pruebas; nil = time.Now.
	Now func() time.Time
}

type UseCase struct {
	entries ports.EntryRepository
	imports ports.ImportRepository
	tx      ports.Transactor
	events  ports.EventPublisher
	logger  *zap.Logger
	now     func() time.Time
}

func New(d Deps) *UseCase {
	now := d.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &UseCase{entries: d.Entries, imports: d.Imports, tx: d.Tx, events: d.Events, logger: d.Logger, now: now}
}

// Check devuelve, de las direcciones dadas, las que estan excluidas y vigentes. Las
// direcciones se normalizan y las que no son direcciones validas se ignoran: no pueden
// estar en la lista y quien envia ya las rechaza por su cuenta.
func (uc *UseCase) Check(ctx context.Context, tenantID uuid.UUID, emails []string) ([]domain.Suppressed, error) {
	if len(emails) > MaxCheckEmails {
		return nil, domain.ErrTooManyEmails
	}
	valid, _ := domain.NormalizeEmails(emails)
	if len(valid) == 0 {
		return []domain.Suppressed{}, nil
	}
	found, err := uc.entries.FindByEmails(ctx, tenantID, valid)
	if err != nil {
		return nil, err
	}
	now := uc.now()
	out := make([]domain.Suppressed, 0, len(found))
	for _, e := range found {
		if e.Active(now) {
			out = append(out, domain.Suppressed{Email: e.Email, Reason: e.Reason})
		}
	}
	return out, nil
}

// AddInput es lo que registra la plataforma por el endpoint interno o por ingesta.
type AddInput struct {
	Email      string
	Reason     domain.Reason
	Source     string
	Detail     string
	MessageID  *uuid.UUID
	CampaignID *uuid.UUID
}

// Add registra una exclusion de forma idempotente. Si la direccion ya esta con una causa
// igual o mas grave, no cambia nada y devuelve la fila existente (added=false). Si esta
// con una causa menor, la eleva a la nueva y retira cualquier caducidad: un rebote duro
// sobre una exclusion manual temporal la convierte en definitiva.
func (uc *UseCase) Add(ctx context.Context, tenantID uuid.UUID, in AddInput) (*domain.Entry, bool, error) {
	email, err := domain.NormalizeEmail(in.Email)
	if err != nil {
		return nil, false, err
	}
	if _, err := domain.ParseReason(string(in.Reason)); err != nil {
		return nil, false, err
	}
	var (
		out   *domain.Entry
		added bool
	)
	// Dos registros simultaneos de una direccion NUEVA: el bloqueo de fila no la cubre
	// porque aun no existia y el segundo Insert choca con el UNIQUE. Se repite una vez;
	// a la segunda la fila ya existe y entra por la rama de comparacion.
	for attempt := 0; attempt < 2; attempt++ {
		err = uc.tx.Transact(ctx, func(ctx context.Context) error {
			return uc.addInTx(ctx, tenantID, email, in, &out, &added)
		})
		if !errors.Is(err, domain.ErrEntryAlreadyExists) {
			break
		}
	}
	if err != nil {
		return nil, false, err
	}
	return out, added, nil
}

// addInTx es el cuerpo transaccional de Add: compara con la fila existente (bloqueada) o
// inserta la nueva, y deja en out/added el resultado.
func (uc *UseCase) addInTx(ctx context.Context, tenantID uuid.UUID, email string, in AddInput, out **domain.Entry, added *bool) error {
	existing, err := uc.entries.GetByEmailForUpdate(ctx, tenantID, email)
	switch {
	case err == nil:
		if existing.Reason.Severity() >= in.Reason.Severity() {
			*out, *added = existing, false
			return nil
		}
		existing.Reason = in.Reason
		existing.Source = in.Source
		existing.Detail = in.Detail
		existing.MessageID = in.MessageID
		existing.CampaignID = in.CampaignID
		existing.ExpiresAt = nil
		if err := uc.entries.Update(ctx, existing); err != nil {
			return err
		}
		*out, *added = existing, true
		return uc.events.EntryAdded(ctx, existing)
	case errors.Is(err, domain.ErrEntryNotFound):
		e := &domain.Entry{
			TenantID: tenantID, Email: email, Reason: in.Reason, Source: in.Source,
			Detail: in.Detail, MessageID: in.MessageID, CampaignID: in.CampaignID,
		}
		if err := uc.entries.Insert(ctx, e); err != nil {
			return err
		}
		*out, *added = e, true
		return uc.events.EntryAdded(ctx, e)
	default:
		return err
	}
}

// CreateManualInput es lo que un operador registra por el API publico.
type CreateManualInput struct {
	Email     string
	Reason    domain.Reason
	Detail    string
	ExpiresAt *time.Time
}

// CreateManual registra una exclusion manual. Solo manual: las demas causas las
// registra la plataforma a partir de hechos (rebotes, quejas, bajas), no una persona.
func (uc *UseCase) CreateManual(ctx context.Context, tenantID uuid.UUID, in CreateManualInput) (*domain.Entry, error) {
	email, err := domain.NormalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}
	if in.Reason != domain.ReasonManual {
		return nil, domain.ErrManualOnly
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(uc.now()) {
		return nil, domain.ErrExpiryInPast
	}
	e := &domain.Entry{
		TenantID: tenantID, Email: email, Reason: domain.ReasonManual,
		Source: SourceAPI, Detail: in.Detail, ExpiresAt: in.ExpiresAt,
	}
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.entries.Insert(ctx, e); err != nil {
			return err
		}
		return uc.events.EntryAdded(ctx, e)
	})
	if err != nil {
		return nil, err
	}
	return e, nil
}

// Remove retira una exclusion. Una baja pedida por la persona no se retira por aqui;
// las demas quedan auditadas por el evento suppression.entry.removed, que lleva a quien
// lo hizo.
func (uc *UseCase) Remove(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		e, err := uc.entries.GetByID(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if !e.Reason.Removable() {
			return domain.ErrUnsubscribeProtected
		}
		if err := uc.entries.Delete(ctx, tenantID, id); err != nil {
			return err
		}
		return uc.events.EntryRemoved(ctx, e)
	})
}

// Resubscribe levanta la baja de una direccion cuando contacts registra un nuevo
// consentimiento explicito. Solo retira una exclusion por unsubscribe: un rebote duro o
// una queja siguen vigentes aunque la persona vuelva a consentir. Idempotente.
func (uc *UseCase) Resubscribe(ctx context.Context, tenantID uuid.UUID, rawEmail string) (bool, error) {
	email, err := domain.NormalizeEmail(rawEmail)
	if err != nil {
		return false, err
	}
	removed := false
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		e, err := uc.entries.GetByEmailForUpdate(ctx, tenantID, email)
		if errors.Is(err, domain.ErrEntryNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if e.Reason != domain.ReasonUnsubscribe {
			return nil
		}
		if err := uc.entries.Delete(ctx, tenantID, e.ID); err != nil {
			return err
		}
		removed = true
		return uc.events.EntryRemoved(ctx, e)
	})
	return removed, err
}

func (uc *UseCase) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Entry, error) {
	return uc.entries.GetByID(ctx, tenantID, id)
}

func (uc *UseCase) List(ctx context.Context, tenantID uuid.UUID, f ports.ListFilter) ([]domain.Entry, int64, error) {
	if f.Reason != "" {
		if _, err := domain.ParseReason(string(f.Reason)); err != nil {
			return nil, 0, err
		}
	}
	return uc.entries.List(ctx, tenantID, f)
}

// ImportInput es una carga masiva de exclusiones manuales.
type ImportInput struct {
	Emails    []string
	Reason    domain.Reason
	Detail    string
	CreatedBy uuid.UUID
}

// Import registra en bloque las direcciones que aun no estan excluidas y deja el rastro
// de la carga. Las invalidas, las repetidas dentro de la lista y las que ya estaban se
// cuentan como omitidas; no interrumpen la carga.
func (uc *UseCase) Import(ctx context.Context, tenantID uuid.UUID, in ImportInput) (*domain.Import, error) {
	if len(in.Emails) == 0 {
		return nil, domain.ErrNoEmails
	}
	if len(in.Emails) > MaxImportEmails {
		return nil, domain.ErrTooManyEmails
	}
	if in.Reason != domain.ReasonManual {
		return nil, domain.ErrManualOnly
	}
	valid, discarded := domain.NormalizeEmails(in.Emails)
	imp := &domain.Import{TenantID: tenantID, Total: len(in.Emails), CreatedBy: in.CreatedBy}
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		var added []domain.Entry
		if len(valid) > 0 {
			var err error
			added, err = uc.entries.InsertMissing(ctx, tenantID, valid, domain.ReasonManual, SourceImport, in.Detail)
			if err != nil {
				return err
			}
		}
		imp.Added = len(added)
		imp.Skipped = discarded + (len(valid) - len(added))
		for i := range added {
			if err := uc.events.EntryAdded(ctx, &added[i]); err != nil {
				return err
			}
		}
		return uc.imports.Create(ctx, imp)
	})
	if err != nil {
		return nil, err
	}
	return imp, nil
}

func (uc *UseCase) ListImports(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.Import, int64, error) {
	return uc.imports.List(ctx, tenantID, page, perPage)
}

// Stats es el conteo de exclusiones vigentes por causa; todas las causas aparecen,
// aunque sea con cero.
type Stats struct {
	Total    int64            `json:"total"`
	ByReason map[string]int64 `json:"by_reason"`
}

func (uc *UseCase) Stats(ctx context.Context, tenantID uuid.UUID) (*Stats, error) {
	counts, err := uc.entries.CountByReason(ctx, tenantID, uc.now())
	if err != nil {
		return nil, err
	}
	s := &Stats{ByReason: make(map[string]int64, len(domain.Reasons()))}
	for _, r := range domain.Reasons() {
		s.ByReason[string(r)] = 0
	}
	for _, c := range counts {
		s.ByReason[string(c.Reason)] = c.Count
		s.Total += c.Count
	}
	return s, nil
}
