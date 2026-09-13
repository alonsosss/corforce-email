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

// addresses lee todas las causas de las direcciones dadas y las agrupa por direccion.
func (uc *UseCase) addresses(ctx context.Context, tenantID uuid.UUID, emails []string, now time.Time) (map[string]domain.Address, error) {
	found, err := uc.entries.FindByEmails(ctx, tenantID, emails)
	if err != nil {
		return nil, err
	}
	out := make(map[string]domain.Address, len(emails))
	for _, a := range domain.Aggregate(found, now) {
		out[a.Email] = a
	}
	return out, nil
}

// address devuelve una direccion con todas sus causas; domain.ErrEntryNotFound si no
// tiene ninguna.
func (uc *UseCase) address(ctx context.Context, tenantID uuid.UUID, email string, now time.Time) (*domain.Address, error) {
	byEmail, err := uc.addresses(ctx, tenantID, []string{email}, now)
	if err != nil {
		return nil, err
	}
	a, ok := byEmail[email]
	if !ok {
		return nil, domain.ErrEntryNotFound
	}
	return &a, nil
}

// activeReasons son las causas vigentes de una direccion; vacio si quedo libre.
func (uc *UseCase) activeReasons(ctx context.Context, tenantID uuid.UUID, email string, now time.Time) ([]domain.Reason, error) {
	a, err := uc.address(ctx, tenantID, email, now)
	if errors.Is(err, domain.ErrEntryNotFound) {
		return []domain.Reason{}, nil
	}
	if err != nil {
		return nil, err
	}
	return a.Reasons, nil
}

// Check devuelve, de las direcciones dadas, las que tienen alguna causa vigente, con la
// principal como reason y todas las vigentes en reasons. Las direcciones se normalizan y
// las que no son direcciones validas se ignoran: no pueden estar en la lista y quien envia
// ya las rechaza por su cuenta.
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
	addresses := domain.Aggregate(found, uc.now())
	out := make([]domain.Suppressed, 0, len(addresses))
	for _, a := range addresses {
		if a.Active() {
			out = append(out, domain.Suppressed{Email: a.Email, Reason: a.Reason, Reasons: a.Reasons})
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

// Add registra una causa de exclusion de forma idempotente. Cada causa se guarda aparte:
// una baja no absorbe una exclusion manual ni un rebote duro una baja. Si la direccion ya
// tiene esa causa vigente no cambia nada (added=false); si la tenia caducada (solo una
// manual caduca) la reactiva sin caducidad. Devuelve la direccion con todas sus causas.
func (uc *UseCase) Add(ctx context.Context, tenantID uuid.UUID, in AddInput) (*domain.Address, bool, error) {
	email, err := domain.NormalizeEmail(in.Email)
	if err != nil {
		return nil, false, err
	}
	if _, err := domain.ParseReason(string(in.Reason)); err != nil {
		return nil, false, err
	}
	var (
		out   *domain.Address
		added bool
	)
	// El bloqueo de la direccion serializa las altas por esta via, pero una carga masiva
	// no lo toma: si inserta la misma causa a la vez, el Insert choca con el UNIQUE. Se
	// repite una vez; a la segunda la fila ya existe y entra por la rama de comparacion.
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

// addInTx es el cuerpo transaccional de Add: con la direccion bloqueada, deja la causa
// como esta si ya es vigente, la reactiva si caduco o la inserta, y deja en out/added el
// resultado.
func (uc *UseCase) addInTx(ctx context.Context, tenantID uuid.UUID, email string, in AddInput, out **domain.Address, added *bool) error {
	if err := uc.entries.LockAddress(ctx, tenantID, email); err != nil {
		return err
	}
	now := uc.now()
	var changed *domain.Entry
	existing, err := uc.entries.GetCauseForUpdate(ctx, tenantID, email, in.Reason)
	switch {
	case err == nil && existing.Active(now):
	case err == nil:
		existing.Source = in.Source
		existing.Detail = in.Detail
		existing.MessageID = in.MessageID
		existing.CampaignID = in.CampaignID
		existing.ExpiresAt = nil
		if err := uc.entries.Update(ctx, existing); err != nil {
			return err
		}
		changed = existing
	case errors.Is(err, domain.ErrEntryNotFound):
		e := &domain.Entry{
			TenantID: tenantID, Email: email, Reason: in.Reason, Source: in.Source,
			Detail: in.Detail, MessageID: in.MessageID, CampaignID: in.CampaignID,
		}
		if err := uc.entries.Insert(ctx, e); err != nil {
			return err
		}
		changed = e
	default:
		return err
	}
	addr, err := uc.address(ctx, tenantID, email, now)
	if err != nil {
		return err
	}
	*out, *added = addr, changed != nil
	if changed == nil {
		return nil
	}
	return uc.events.EntryAdded(ctx, changed, addr.Reasons)
}

// CreateManualInput es lo que un operador registra por el API publico.
type CreateManualInput struct {
	Email     string
	Reason    domain.Reason
	Detail    string
	ExpiresAt *time.Time
}

// CreateManual registra una exclusion manual, se sume o no a otras causas de la
// direccion. Solo manual: las demas causas las registra la plataforma a partir de hechos
// (rebotes, quejas, bajas), no una persona. Una exclusion manual vigente de la misma
// direccion es un conflicto; una caducada se renueva con los datos nuevos, porque ya no
// excluia a nadie y conservarla obligaria a borrarla antes de volver a excluir.
func (uc *UseCase) CreateManual(ctx context.Context, tenantID uuid.UUID, in CreateManualInput) (*domain.Address, error) {
	email, err := domain.NormalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}
	if in.Reason != domain.ReasonManual {
		return nil, domain.ErrManualOnly
	}
	now := uc.now()
	if in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		return nil, domain.ErrExpiryInPast
	}
	var out *domain.Address
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.entries.LockAddress(ctx, tenantID, email); err != nil {
			return err
		}
		e, err := uc.entries.GetCauseForUpdate(ctx, tenantID, email, domain.ReasonManual)
		switch {
		case err == nil && e.Active(now):
			return domain.ErrEntryAlreadyExists
		case err == nil:
			e.Source, e.Detail, e.MessageID, e.CampaignID, e.ExpiresAt = SourceAPI, in.Detail, nil, nil, in.ExpiresAt
			if err := uc.entries.Update(ctx, e); err != nil {
				return err
			}
		case errors.Is(err, domain.ErrEntryNotFound):
			e = &domain.Entry{
				TenantID: tenantID, Email: email, Reason: domain.ReasonManual,
				Source: SourceAPI, Detail: in.Detail, ExpiresAt: in.ExpiresAt,
			}
			if err := uc.entries.Insert(ctx, e); err != nil {
				return err
			}
		default:
			return err
		}
		addr, err := uc.address(ctx, tenantID, email, now)
		if err != nil {
			return err
		}
		out = addr
		return uc.events.EntryAdded(ctx, e, addr.Reasons)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Remove retira UNA causa de exclusion (la fila id); las demas causas de la direccion
// siguen vigentes. Una baja pedida por la persona no se retira por aqui; las demas quedan
// auditadas por el evento suppression.entry.removed, que lleva a quien lo hizo.
func (uc *UseCase) Remove(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.tx.Transact(ctx, func(ctx context.Context) error {
		e, err := uc.entries.GetByID(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if !e.Reason.Removable() {
			return domain.ErrUnsubscribeProtected
		}
		return uc.removeCause(ctx, e)
	})
}

// removeCause borra la fila con la direccion bloqueada y publica las causas que quedan.
func (uc *UseCase) removeCause(ctx context.Context, e *domain.Entry) error {
	if err := uc.entries.LockAddress(ctx, e.TenantID, e.Email); err != nil {
		return err
	}
	if err := uc.entries.Delete(ctx, e.TenantID, e.ID); err != nil {
		return err
	}
	reasons, err := uc.activeReasons(ctx, e.TenantID, e.Email, uc.now())
	if err != nil {
		return err
	}
	return uc.events.EntryRemoved(ctx, e, reasons)
}

// Resubscribe levanta la baja de una direccion cuando contacts registra un nuevo
// consentimiento explicito. Solo retira la causa unsubscribe: un rebote duro, una queja o
// una exclusion manual siguen vigentes aunque la persona vuelva a consentir. Idempotente.
func (uc *UseCase) Resubscribe(ctx context.Context, tenantID uuid.UUID, rawEmail string) (bool, error) {
	email, err := domain.NormalizeEmail(rawEmail)
	if err != nil {
		return false, err
	}
	removed := false
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.entries.LockAddress(ctx, tenantID, email); err != nil {
			return err
		}
		e, err := uc.entries.GetCauseForUpdate(ctx, tenantID, email, domain.ReasonUnsubscribe)
		if errors.Is(err, domain.ErrEntryNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := uc.removeCause(ctx, e); err != nil {
			return err
		}
		removed = true
		return nil
	})
	return removed, err
}

// Get devuelve la direccion a la que pertenece la causa id, con todas sus causas.
func (uc *UseCase) Get(ctx context.Context, tenantID, id uuid.UUID) (*domain.Address, error) {
	e, err := uc.entries.GetByID(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	return uc.address(ctx, tenantID, e.Email, uc.now())
}

// List pagina las direcciones excluidas. El filtro por causa se aplica a la principal.
func (uc *UseCase) List(ctx context.Context, tenantID uuid.UUID, f ports.ListFilter) ([]domain.Address, int64, error) {
	if f.Reason != "" {
		if _, err := domain.ParseReason(string(f.Reason)); err != nil {
			return nil, 0, err
		}
	}
	now := uc.now()
	emails, total, err := uc.entries.ListAddresses(ctx, tenantID, f, now)
	if err != nil {
		return nil, 0, err
	}
	out := make([]domain.Address, 0, len(emails))
	if len(emails) == 0 {
		return out, total, nil
	}
	byEmail, err := uc.addresses(ctx, tenantID, emails, now)
	if err != nil {
		return nil, 0, err
	}
	// Una direccion que perdio su ultima causa entre las dos lecturas no se devuelve.
	for _, email := range emails {
		if a, ok := byEmail[email]; ok {
			out = append(out, a)
		}
	}
	return out, total, nil
}

// ImportInput es una carga masiva de exclusiones manuales.
type ImportInput struct {
	Emails    []string
	Reason    domain.Reason
	Detail    string
	CreatedBy uuid.UUID
}

// Import registra en bloque la exclusion manual de las direcciones que aun no la tienen
// vigente (aunque ya esten excluidas por otra causa) y deja el rastro de la carga. Las
// invalidas, las repetidas dentro de la lista y las que ya tenian una exclusion manual
// vigente se cuentan como omitidas; no interrumpen la carga.
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
	now := uc.now()
	err := uc.tx.Transact(ctx, func(ctx context.Context) error {
		var added []domain.Entry
		if len(valid) > 0 {
			var err error
			added, err = uc.entries.InsertMissing(ctx, tenantID, valid, domain.ReasonManual, SourceImport, in.Detail, now)
			if err != nil {
				return err
			}
		}
		imp.Added = len(added)
		imp.Skipped = discarded + (len(valid) - len(added))
		if len(added) > 0 {
			emails := make([]string, len(added))
			for i := range added {
				emails[i] = added[i].Email
			}
			byEmail, err := uc.addresses(ctx, tenantID, emails, now)
			if err != nil {
				return err
			}
			for i := range added {
				if err := uc.events.EntryAdded(ctx, &added[i], byEmail[added[i].Email].Reasons); err != nil {
					return err
				}
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

// Stats cuenta las direcciones con alguna exclusion vigente, repartidas por su causa
// principal (la suma de by_reason es total); todas las causas aparecen, aunque sea con
// cero.
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
