package app

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// importBatchSize es el tamano de cada transaccion de la importacion: un fallo a
	// mitad deja confirmados los lotes anteriores y no retiene bloqueos sobre miles de
	// filas a la vez. Es tambien la consulta de cada lote a suppression, por debajo de su
	// tope de 1000 direcciones.
	importBatchSize = 500
	// MaxImportErrors es el tope de filas de error guardadas y devueltas.
	MaxImportErrors = 100
	// MaxConsentBasis acota el texto de la base legal declarada.
	MaxConsentBasis = 2000
)

// ImportRow es una fila tal como llega. Los campos vacios no cambian nada de un contacto
// existente: una importacion no borra datos.
type ImportRow struct {
	Email      string
	FirstName  string
	LastName   string
	Locale     string
	Timezone   string
	Attributes domain.RawAttributes
	Tags       []string
}

type ImportInput struct {
	Rows           []ImportRow
	ListID         *uuid.UUID
	UpdateExisting bool
	// GrantConsent registra consentimiento concedido (method import) con la base legal
	// declarada, solo a los contactos que quedan active y sin baja vigente
	// (domain.ImportMayGrant).
	GrantConsent bool
	ConsentBasis string
	CreatedBy    uuid.UUID
}

// importRow es una fila ya normalizada, con su numero de linea.
type importRow struct {
	line    int
	contact domain.Contact
	attrs   map[string]any
}

// batchResult son los conteos de un lote; se suman a la importacion solo si el lote
// confirmo.
type batchResult struct {
	created, updated, skipped int
	errors                    []domain.ImportError
	suppressed                map[domain.Status]int
}

func (uc *UseCase) Import(ctx context.Context, tenantID uuid.UUID, in ImportInput) (*domain.Import, error) {
	if len(in.Rows) == 0 {
		return nil, domain.ErrNoImportRows
	}
	if len(in.Rows) > uc.cfg.ImportMaxRows {
		return nil, domain.ErrTooManyRows
	}
	basis := strings.TrimSpace(in.ConsentBasis)
	if in.GrantConsent && (basis == "" || utf8.RuneCountInString(basis) > MaxConsentBasis) {
		return nil, domain.ErrConsentBasis
	}
	if !in.GrantConsent {
		basis = ""
	}
	if in.ListID != nil {
		if _, err := uc.lists.Get(ctx, tenantID, *in.ListID); err != nil {
			return nil, err
		}
	}
	defs, err := uc.definitions(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	imp := &domain.Import{
		ID: uuid.New(), TenantID: tenantID, Status: domain.ImportCompleted, Total: len(in.Rows),
		ConsentBasis: basis, ListID: in.ListID, CreatedBy: in.CreatedBy, Errors: []domain.ImportError{},
		Suppressed: map[domain.Status]int{},
	}
	rows := prepareRows(in.Rows, defs, imp)

	// Cada lote lee sus causas vigentes justo antes de su transaccion: una causa registrada
	// despues llega por evento al contacto ya confirmado. Si suppression no responde, el
	// lote no se escribe y la importacion se corta como ante cualquier fallo a mitad: quedan
	// los lotes anteriores, cada uno comprobado, y el rastro queda failed.
	for start := 0; start < len(rows); start += importBatchSize {
		batch := rows[start:min(start+importBatchSize, len(rows))]
		var res batchResult
		active, err := uc.admissionCauses(ctx, tenantID, emailsOf(batch))
		if err == nil {
			err = uc.tx.Transact(ctx, func(ctx context.Context) error {
				res = batchResult{suppressed: map[domain.Status]int{}}
				return uc.importBatch(ctx, tenantID, in, basis, imp.ID, defs, batch, active, &res)
			})
		}
		if err != nil {
			imp.Status = domain.ImportFailed
			uc.saveFailedImport(ctx, imp)
			return nil, fmt.Errorf("importacion %s: lote desde la fila %d: %w", imp.ID, batch[0].line, err)
		}
		imp.Created += res.created
		imp.Updated += res.updated
		imp.Skipped += res.skipped
		for _, e := range res.errors {
			addImportError(imp, e)
		}
		for st, n := range res.suppressed {
			imp.Suppressed[st] += n
		}
	}

	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		if err := uc.imports.Create(ctx, imp); err != nil {
			return err
		}
		return uc.events.ImportCompleted(ctx, imp)
	})
	if err != nil {
		return nil, err
	}
	return imp, nil
}

func addImportError(imp *domain.Import, e domain.ImportError) {
	if len(imp.Errors) < MaxImportErrors {
		imp.Errors = append(imp.Errors, e)
	}
}

// prepareRows normaliza y valida cada fila; las invalidas y las repetidas cuentan como
// omitidas con su motivo. Los atributos null se ignoran: en una importacion no borran.
func prepareRows(in []ImportRow, defs domain.Definitions, imp *domain.Import) []importRow {
	out := make([]importRow, 0, len(in))
	seen := make(map[string]int, len(in))
	reject := func(line int, reason string) {
		imp.Skipped++
		addImportError(imp, domain.ImportError{Line: line, Reason: reason})
	}
	for i, r := range in {
		line := i + 1
		email, err := domain.NormalizeEmail(r.Email)
		if err != nil {
			reject(line, err.Error())
			continue
		}
		if prev, dup := seen[email]; dup {
			reject(line, fmt.Sprintf("direccion repetida en la importacion (linea %d)", prev))
			continue
		}
		c := domain.Contact{Email: email, Status: domain.StatusActive, ConsentStatus: domain.ConsentNone, Source: domain.SourceImport}
		if c.FirstName, err = domain.NormalizeName(r.FirstName); err != nil {
			reject(line, "first_name: "+err.Error())
			continue
		}
		if c.LastName, err = domain.NormalizeName(r.LastName); err != nil {
			reject(line, "last_name: "+err.Error())
			continue
		}
		if c.Locale, err = domain.NormalizeLocale(r.Locale); err != nil {
			reject(line, err.Error())
			continue
		}
		if c.Timezone, err = domain.NormalizeTimezone(r.Timezone); err != nil {
			reject(line, err.Error())
			continue
		}
		if c.Tags, err = domain.NormalizeTags(r.Tags); err != nil {
			reject(line, err.Error())
			continue
		}
		provided := make(domain.RawAttributes, len(r.Attributes))
		for k, v := range r.Attributes {
			if t := bytes.TrimSpace(v); len(t) > 0 && !bytes.Equal(t, []byte("null")) {
				provided[k] = v
			}
		}
		attrs, _, err := domain.MergeAttributes(nil, provided, defs)
		if err != nil {
			reject(line, err.Error())
			continue
		}
		seen[email] = line
		out = append(out, importRow{line: line, contact: c, attrs: attrs})
	}
	return out
}

func emailsOf(batch []importRow) []string {
	out := make([]string, len(batch))
	for i, r := range batch {
		out[i] = r.contact.Email
	}
	return out
}

// importBatch aplica un lote dentro de su transaccion. Los contactos existentes quedan
// bloqueados desde la lectura, asi que el estado con el que se decide si se concede el
// consentimiento es el que se confirma; su estado no se toca (lo fijan los eventos de
// suppression). Los nuevos entran con el estado que implican sus causas vigentes (active,
// de suppression, por direccion).
func (uc *UseCase) importBatch(ctx context.Context, tenantID uuid.UUID, in ImportInput, basis string, importID uuid.UUID, defs domain.Definitions, batch []importRow, active map[string][]domain.ActiveCause, res *batchResult) error {
	existing, err := uc.contacts.FindByEmailsForUpdate(ctx, tenantID, emailsOf(batch))
	if err != nil {
		return err
	}
	byEmail := make(map[string]domain.Contact, len(existing))
	for _, c := range existing {
		byEmail[c.Email] = c
	}

	var (
		inserts, updates []domain.Contact
		grants           []domain.Consent
		members          []uuid.UUID
	)
	grant := func(contactID uuid.UUID) {
		grants = append(grants, domain.Consent{
			TenantID: tenantID, ContactID: contactID, Purpose: domain.PurposeMarketing,
			Status: domain.ConsentGranted, Method: domain.MethodImport, Source: "import:" + importID.String(),
			Evidence: map[string]any{"basis": basis, "import_id": importID.String()},
		})
	}
	for _, r := range batch {
		if ex, ok := byEmail[r.contact.Email]; ok {
			if !in.UpdateExisting {
				res.skipped++
				continue
			}
			merged, err := mergeImported(ex, r)
			if err != nil {
				res.skipped++
				res.errors = append(res.errors, domain.ImportError{Line: r.line, Reason: err.Error()})
				continue
			}
			updates = append(updates, merged)
			res.updated++
			if in.GrantConsent && domain.ImportMayGrant(&ex, active[ex.Email]) {
				grant(ex.ID)
			}
			if in.ListID != nil {
				members = append(members, ex.ID)
			}
			continue
		}
		if err := domain.CheckRequired(r.attrs, defs); err != nil {
			res.skipped++
			res.errors = append(res.errors, domain.ImportError{Line: r.line, Reason: err.Error()})
			continue
		}
		c := r.contact
		c.TenantID = tenantID
		c.Attributes = r.attrs
		c.AdmitSuppression(active[c.Email])
		inserts = append(inserts, c)
	}

	if len(inserts) > 0 {
		inserted, err := uc.contacts.InsertMany(ctx, inserts)
		if err != nil {
			return err
		}
		res.created += len(inserted)
		// Una direccion que otro creo entre la lectura y la escritura no se pisa.
		res.skipped += len(inserts) - len(inserted)
		for _, c := range inserted {
			if c.Status != domain.StatusActive {
				res.suppressed[c.Status]++
			}
			if in.GrantConsent && domain.ImportMayGrant(&c, active[c.Email]) {
				grant(c.ID)
			}
			if in.ListID != nil {
				members = append(members, c.ID)
			}
		}
	}
	if len(updates) > 0 {
		if err := uc.contacts.UpdateMany(ctx, updates); err != nil {
			return err
		}
	}
	if len(grants) > 0 {
		if err := uc.consents.AppendMany(ctx, grants); err != nil {
			return err
		}
	}
	if in.ListID != nil && len(members) > 0 {
		if _, err := uc.lists.AddMembers(ctx, tenantID, *in.ListID, members); err != nil {
			return err
		}
	}
	return nil
}

// mergeImported aplica sobre un contacto existente lo que trae la fila: los campos con
// valor, los atributos presentes y las etiquetas nuevas. El estado no se toca.
func mergeImported(ex domain.Contact, r importRow) (domain.Contact, error) {
	if r.contact.FirstName != "" {
		ex.FirstName = r.contact.FirstName
	}
	if r.contact.LastName != "" {
		ex.LastName = r.contact.LastName
	}
	if r.contact.Locale != nil {
		ex.Locale = r.contact.Locale
	}
	if r.contact.Timezone != nil {
		ex.Timezone = r.contact.Timezone
	}
	attrs := make(map[string]any, len(ex.Attributes)+len(r.attrs))
	for k, v := range ex.Attributes {
		attrs[k] = v
	}
	for k, v := range r.attrs {
		attrs[k] = v
	}
	ex.Attributes = attrs
	tags, err := domain.MergeTags(ex.Tags, r.contact.Tags)
	if err != nil {
		return domain.Contact{}, err
	}
	ex.Tags = tags
	return ex, nil
}

// saveFailedImport deja el rastro de una importacion interrumpida con lo que alcanzo a
// confirmar. Usa un contexto propio: si la peticion se cancelo, el rastro igual se guarda.
func (uc *UseCase) saveFailedImport(ctx context.Context, imp *domain.Import) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := uc.tx.Transact(ctx, func(ctx context.Context) error { return uc.imports.Create(ctx, imp) }); err != nil {
		uc.logger.Error("contacts: no se pudo guardar el rastro de la importacion fallida",
			zap.String("import_id", imp.ID.String()), zap.Error(err))
	}
}

func (uc *UseCase) GetImport(ctx context.Context, tenantID, id uuid.UUID) (*domain.Import, error) {
	return uc.imports.Get(ctx, tenantID, id)
}

func (uc *UseCase) ListImports(ctx context.Context, tenantID uuid.UUID, page, perPage int) ([]domain.Import, int64, error) {
	return uc.imports.List(ctx, tenantID, page, perPage)
}
