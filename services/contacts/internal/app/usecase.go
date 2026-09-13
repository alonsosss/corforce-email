package app

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"time"

	"github.com/alonsosss/corforce-email/services/contacts/internal/domain"
	"github.com/alonsosss/corforce-email/services/contacts/internal/ports"
	"github.com/alonsosss/corforce-email/services/contacts/internal/segment"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// DefaultDOITTL es la vigencia de un enlace de doble opt-in (CONTACTS_DOI_TTL).
	DefaultDOITTL = 72 * time.Hour
	// DefaultImportMaxRows es el tope de filas por importacion (CONTACTS_IMPORT_MAX_ROWS).
	DefaultImportMaxRows = 50000
	// segmentQueryTimeout acota la evaluacion de un segmento o de una pagina de
	// audiencia: una definicion costosa no debe retener una conexion indefinidamente.
	segmentQueryTimeout = 20 * time.Second
)

// Config es la configuracion de negocio que llega del entorno.
type Config struct {
	// PublicBaseURL es la URL publica de la plataforma, sin barra final; de ella cuelga
	// el enlace de confirmacion del doble opt-in.
	PublicBaseURL string
	DOITTL        time.Duration
	ImportMaxRows int
}

type Deps struct {
	Contacts   ports.ContactRepository
	Consents   ports.ConsentRepository
	Tokens     ports.TokenRepository
	Lists      ports.ListRepository
	Attributes ports.AttributeRepository
	Segments   ports.SegmentRepository
	Query      ports.SegmentQuery
	Imports    ports.ImportRepository
	Tx         ports.Transactor
	Events     ports.EventPublisher
	Config     Config
	Logger     *zap.Logger
	// Now fija el reloj en las pruebas; nil = time.Now en UTC.
	Now func() time.Time
	// Random es la fuente de los tokens; nil = crypto/rand.
	Random io.Reader
}

type UseCase struct {
	contacts   ports.ContactRepository
	consents   ports.ConsentRepository
	tokens     ports.TokenRepository
	lists      ports.ListRepository
	attributes ports.AttributeRepository
	segments   ports.SegmentRepository
	query      ports.SegmentQuery
	imports    ports.ImportRepository
	tx         ports.Transactor
	events     ports.EventPublisher
	cfg        Config
	logger     *zap.Logger
	now        func() time.Time
	random     io.Reader
}

func New(d Deps) *UseCase {
	now := d.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	random := d.Random
	if random == nil {
		random = rand.Reader
	}
	cfg := d.Config
	if cfg.DOITTL <= 0 {
		cfg.DOITTL = DefaultDOITTL
	}
	if cfg.ImportMaxRows <= 0 {
		cfg.ImportMaxRows = DefaultImportMaxRows
	}
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &UseCase{
		contacts: d.Contacts, consents: d.Consents, tokens: d.Tokens, lists: d.Lists,
		attributes: d.Attributes, segments: d.Segments, query: d.Query, imports: d.Imports,
		tx: d.Tx, events: d.Events, cfg: cfg, logger: logger, now: now, random: random,
	}
}

// ImportMaxRows expone el tope efectivo para que el handler dimensione el cuerpo.
func (uc *UseCase) ImportMaxRows() int { return uc.cfg.ImportMaxRows }

func (uc *UseCase) definitions(ctx context.Context, tenantID uuid.UUID) (domain.Definitions, error) {
	defs, err := uc.attributes.List(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return domain.IndexDefinitions(defs), nil
}

// schemaFor arma lo que un segmento puede usar: los atributos declarados y los valores
// de los campos enumerados, tomados del dominio (una sola fuente de verdad).
func schemaFor(defs domain.Definitions) segment.Schema {
	attrs := make(map[string]segment.AttrType, len(defs))
	for k, d := range defs {
		attrs[k] = segment.AttrType(d.Type)
	}
	statuses := make([]string, 0, len(domain.Statuses()))
	for _, s := range domain.Statuses() {
		statuses = append(statuses, string(s))
	}
	sources := make([]string, 0, len(domain.Sources()))
	for _, s := range domain.Sources() {
		sources = append(sources, string(s))
	}
	return segment.Schema{
		Attributes: attrs,
		Enums: map[string][]string{
			"status": statuses,
			"source": sources,
			"consent": {
				string(domain.ConsentGranted), string(domain.ConsentRevoked),
				string(domain.ConsentPending), string(domain.ConsentNone),
			},
		},
	}
}

// segmentError conserva el mensaje del compilador (que dice que regla falla) y se
// reconoce como domain.ErrInvalidSegment.
type segmentError struct{ err error }

func (e segmentError) Error() string        { return e.err.Error() }
func (e segmentError) Is(target error) bool { return target == domain.ErrInvalidSegment }
func (e segmentError) Unwrap() error        { return e.err }

func asSegmentError(err error) error {
	if errors.Is(err, segment.ErrInvalid) {
		return segmentError{err: err}
	}
	return err
}

func dedupeIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
