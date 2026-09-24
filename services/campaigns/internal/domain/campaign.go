// Package domain contiene el modelo de campanas de marketing: su ciclo de vida, los
// lotes en que se reparte la audiencia y las estadisticas que alimentan los eventos de
// entrega. No conoce la base, el bus ni HTTP.
package domain

import (
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Status string

const (
	StatusDraft     Status = "draft"
	StatusScheduled Status = "scheduled"
	StatusSending   Status = "sending"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusCancelled Status = "cancelled"
	StatusFailed    Status = "failed"
)

func Statuses() []Status {
	return []Status{StatusDraft, StatusScheduled, StatusSending, StatusPaused, StatusCompleted, StatusCancelled, StatusFailed}
}

func ParseStatus(s string) (Status, error) {
	for _, st := range Statuses() {
		if string(st) == s {
			return st, nil
		}
	}
	return "", NewValidationError("status: valor no válido %q", s)
}

const (
	MaxNameLength        = 200
	MaxDescriptionLength = 2000
	MaxDisplayNameLength = 200
	MaxEmailLength       = 320
	// MaxAudienceIDs acota cada lista de la audiencia (listas, segmentos, exclusiones).
	MaxAudienceIDs = 50
	// MaxReasonLength acota los motivos de pausa, fallo y error de lote que se guardan:
	// vienen de servicios ajenos y no deben crecer sin limite.
	MaxReasonLength = 1000

	MinScheduleLead    = time.Minute
	MaxScheduleHorizon = 365 * 24 * time.Hour
)

// PauseReasonManual es el motivo de una pausa pedida por una persona.
const PauseReasonManual = "manual"

// Audience es la definicion de a quien va la campana; la resuelve contacts en cada
// lote, que solo devuelve contactos activos con consentimiento vigente.
type Audience struct {
	ListIDs           []uuid.UUID `json:"list_ids"`
	SegmentIDs        []uuid.UUID `json:"segment_ids"`
	ExcludeSegmentIDs []uuid.UUID `json:"exclude_segment_ids"`
}

// Normalized devuelve la audiencia con listas vacias en lugar de nil: se guarda y se
// envia como [] y nunca como null.
func (a Audience) Normalized() Audience {
	norm := func(ids []uuid.UUID) []uuid.UUID {
		if ids == nil {
			return []uuid.UUID{}
		}
		return ids
	}
	return Audience{ListIDs: norm(a.ListIDs), SegmentIDs: norm(a.SegmentIDs), ExcludeSegmentIDs: norm(a.ExcludeSegmentIDs)}
}

func (a Audience) Validate() error {
	if len(a.ListIDs)+len(a.SegmentIDs) == 0 {
		return NewValidationError("audience: incluya al menos una lista o un segmento")
	}
	for field, ids := range map[string][]uuid.UUID{
		"audience.list_ids":            a.ListIDs,
		"audience.segment_ids":         a.SegmentIDs,
		"audience.exclude_segment_ids": a.ExcludeSegmentIDs,
	} {
		if len(ids) > MaxAudienceIDs {
			return NewValidationError("%s: admite como maximo %d elementos", field, MaxAudienceIDs)
		}
		seen := make(map[uuid.UUID]bool, len(ids))
		for _, id := range ids {
			if id == uuid.Nil {
				return NewValidationError("%s: identificador vacio", field)
			}
			if seen[id] {
				return NewValidationError("%s: identificador repetido %s", field, id)
			}
			seen[id] = true
		}
	}
	included := make(map[uuid.UUID]bool, len(a.SegmentIDs))
	for _, id := range a.SegmentIDs {
		included[id] = true
	}
	for _, id := range a.ExcludeSegmentIDs {
		if included[id] {
			return NewValidationError("audience: el segmento %s no puede incluirse y excluirse a la vez", id)
		}
	}
	return nil
}

// Counters son los totales de la campana. targeted, accepted y suppressed los suma el
// orquestador al entregar cada lote; el resto, los eventos de transactional.
type Counters struct {
	Targeted     int64 `json:"targeted"`
	Accepted     int64 `json:"accepted"`
	Suppressed   int64 `json:"suppressed"`
	Sent         int64 `json:"sent"`
	Delivered    int64 `json:"delivered"`
	Bounced      int64 `json:"bounced"`
	Complained   int64 `json:"complained"`
	Opened       int64 `json:"opened"`
	Clicked      int64 `json:"clicked"`
	Unsubscribed int64 `json:"unsubscribed"`
	Failed       int64 `json:"failed"`
}

type Campaign struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	Name          string
	Description   string
	Status        Status
	PauseReason   string
	FailureReason string
	TemplateID    uuid.UUID
	// TemplateVersion se fija al programar o iniciar: una publicacion posterior de la
	// plantilla no cambia lo que recibe una campana en curso.
	TemplateVersion *int
	FromEmail       string
	FromName        string
	ReplyTo         string
	Audience        Audience
	ScheduledAt     *time.Time
	StartedAt       *time.Time
	CompletedAt     *time.Time
	// ResumeAfter aplaza el siguiente lote: lo fija un 429 de transactional (Retry-After)
	// o la espera creciente tras un fallo transitorio.
	ResumeAfter *time.Time
	// ABTest, Resend y TimezoneDelivery son opcionales. ABWinner y ABDecision se fijan una
	// sola vez, al vencer la ventana de decision.
	ABTest           *ABTest
	ABWinner         *int
	ABDecidedAt      *time.Time
	ABDecision       *ABDecision
	Resend           *Resend
	TimezoneDelivery *TimezoneDelivery
	Counters         Counters
	CreatedBy        uuid.UUID
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// NewCampaignInput es lo necesario para crear un borrador.
type NewCampaignInput struct {
	Name        string
	Description string
	TemplateID  uuid.UUID
	FromEmail   string
	FromName    string
	ReplyTo     string
	Audience    Audience
	ABTest      *ABTest
	Resend      *Resend
	CreatedBy   uuid.UUID
}

func NewCampaign(tenantID uuid.UUID, in NewCampaignInput) (*Campaign, error) {
	if in.CreatedBy == uuid.Nil {
		return nil, NewValidationError("created_by: la petición no lleva usuario")
	}
	c := &Campaign{
		ID:          uuid.New(),
		TenantID:    tenantID,
		Name:        strings.TrimSpace(in.Name),
		Description: strings.TrimSpace(in.Description),
		Status:      StatusDraft,
		TemplateID:  in.TemplateID,
		FromEmail:   strings.TrimSpace(in.FromEmail),
		FromName:    strings.TrimSpace(in.FromName),
		ReplyTo:     strings.TrimSpace(in.ReplyTo),
		Audience:    in.Audience.Normalized(),
		ABTest:      cloneABTest(in.ABTest),
		Resend:      cloneResend(in.Resend),
		CreatedBy:   in.CreatedBy,
	}
	if err := c.validateContent(); err != nil {
		return nil, err
	}
	return c, nil
}

// Optional es un campo de PATCH que admite quitarse: Set=false no lo toca; Set=true con
// Value nil lo quita.
type Optional[T any] struct {
	Set   bool
	Value *T
}

// Patch son los cambios de un PATCH; nil = no se toca.
type Patch struct {
	Name        *string
	Description *string
	TemplateID  *uuid.UUID
	FromEmail   *string
	FromName    *string
	ReplyTo     *string
	Audience    *Audience
	ABTest      Optional[ABTest]
	Resend      Optional[Resend]
}

func (p Patch) empty() bool {
	return p.Name == nil && p.Description == nil && p.TemplateID == nil && p.FromEmail == nil &&
		p.FromName == nil && p.ReplyTo == nil && p.Audience == nil && !p.ABTest.Set && !p.Resend.Set
}

// ApplyPatch edita la campana. Solo en borrador o en pausa; en pausa ya hay destinatarios
// que recibieron una version concreta de una plantilla concreta, asi que la audiencia, la
// plantilla, la prueba A/B y el reenvio quedan fijos hasta el final.
func (c *Campaign) ApplyPatch(p Patch) error {
	if !c.Editable() {
		return ErrNotEditable
	}
	if c.ContentLocked() && (p.TemplateID != nil || p.Audience != nil || p.ABTest.Set || p.Resend.Set) {
		return ErrLockedWhilePaused
	}
	if p.empty() {
		return ErrNothingToUpdate
	}
	next := *c
	if p.Name != nil {
		next.Name = strings.TrimSpace(*p.Name)
	}
	if p.Description != nil {
		next.Description = strings.TrimSpace(*p.Description)
	}
	if p.TemplateID != nil {
		next.TemplateID = *p.TemplateID
	}
	if p.FromEmail != nil {
		next.FromEmail = strings.TrimSpace(*p.FromEmail)
	}
	if p.FromName != nil {
		next.FromName = strings.TrimSpace(*p.FromName)
	}
	if p.ReplyTo != nil {
		next.ReplyTo = strings.TrimSpace(*p.ReplyTo)
	}
	if p.Audience != nil {
		next.Audience = p.Audience.Normalized()
	}
	if p.ABTest.Set {
		next.ABTest = cloneABTest(p.ABTest.Value)
	}
	if p.Resend.Set {
		next.Resend = cloneResend(p.Resend.Value)
	}
	if err := next.validateContent(); err != nil {
		return err
	}
	*c = next
	return nil
}

func (c *Campaign) validateContent() error {
	if c.Name == "" {
		return NewValidationError("name: es obligatorio")
	}
	if utf8.RuneCountInString(c.Name) > MaxNameLength {
		return NewValidationError("name: admite como máximo %d caracteres", MaxNameLength)
	}
	if utf8.RuneCountInString(c.Description) > MaxDescriptionLength {
		return NewValidationError("description: admite como máximo %d caracteres", MaxDescriptionLength)
	}
	if c.TemplateID == uuid.Nil {
		return NewValidationError("template_id: es obligatorio")
	}
	if err := validateAddress("from_email", c.FromEmail, true); err != nil {
		return err
	}
	if err := validateDisplayName("from_name", c.FromName); err != nil {
		return err
	}
	if err := validateAddress("reply_to", c.ReplyTo, false); err != nil {
		return err
	}
	if c.ABTest != nil {
		if err := c.ABTest.Validate(c.TemplateID); err != nil {
			return err
		}
		if c.TimezoneDelivery != nil {
			return ErrABWithTimezone
		}
	}
	if c.Resend != nil {
		if err := c.Resend.Validate(); err != nil {
			return err
		}
	}
	return c.Audience.Validate()
}

// cloneABTest copia la configuracion para que la campana no comparta la lista de
// variantes con quien la construyo.
func cloneABTest(a *ABTest) *ABTest {
	if a == nil {
		return nil
	}
	out := *a
	out.Variants = make([]ABVariant, len(a.Variants))
	for i, v := range a.Variants {
		out.Variants[i] = ABVariant{Subject: v.Subject, TemplateID: copyPtr(v.TemplateID),
			TemplateVersion: copyPtr(v.TemplateVersion), PinnedVersion: copyPtr(v.PinnedVersion)}
	}
	out.normalize()
	return &out
}

func cloneResend(r *Resend) *Resend {
	if r == nil {
		return nil
	}
	out := *r
	out.normalize()
	return &out
}

func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// SameContent dice si otra lectura de la campana tiene la misma plantilla y la misma
// prueba A/B: lo comprueba la transicion que fija versiones consultadas fuera de ella.
func (c *Campaign) SameContent(o *Campaign) bool {
	if c.TemplateID != o.TemplateID || (c.ABTest == nil) != (o.ABTest == nil) {
		return false
	}
	if c.ABTest == nil {
		return true
	}
	if len(c.ABTest.Variants) != len(o.ABTest.Variants) {
		return false
	}
	for i := range c.ABTest.Variants {
		a, b := c.ABTest.Variants[i], o.ABTest.Variants[i]
		if a.Subject != b.Subject || !equalPtr(a.TemplateID, b.TemplateID) || !equalPtr(a.TemplateVersion, b.TemplateVersion) {
			return false
		}
	}
	return true
}

func equalPtr[T comparable](a, b *T) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// PinVariants fija la version de cada variante de la prueba A/B (en su orden). Se llama
// junto a Schedule o Start, que exigen todas fijadas.
func (c *Campaign) PinVariants(versions []int) error {
	if c.ABTest == nil {
		if len(versions) > 0 {
			return NewValidationError("ab_test: la campaña no tiene prueba A/B")
		}
		return nil
	}
	if len(versions) != len(c.ABTest.Variants) {
		return NewValidationError("ab_test: faltan versiones de variante")
	}
	for i, v := range versions {
		if err := validVersion(v); err != nil {
			return err
		}
		pinned := v
		c.ABTest.Variants[i].PinnedVersion = &pinned
	}
	return nil
}

// DecideWinner registra la ganadora de la prueba A/B. Solo una vez.
func (c *Campaign) DecideWinner(d ABDecision, now time.Time) error {
	if c.ABTest == nil {
		return NewValidationError("ab_test: la campaña no tiene prueba A/B")
	}
	if c.ABWinner != nil {
		return ErrABAlreadyDecided
	}
	if d.Winner < 0 || d.Winner >= len(c.ABTest.Variants) {
		return NewValidationError("ab_winner: la variante %d no existe", d.Winner)
	}
	w := d.Winner
	c.ABWinner = &w
	c.ABDecidedAt = &now
	c.ABDecision = &d
	return nil
}

// validateAddress acepta solo una direccion desnuda (sin nombre ni angulos): el nombre
// visible va en from_name. Los saltos de linea se rechazan expresamente porque el valor
// termina en una cabecera del mensaje.
func validateAddress(field, v string, required bool) error {
	if v == "" {
		if required {
			return NewValidationError("%s: es obligatorio", field)
		}
		return nil
	}
	if len(v) > MaxEmailLength || strings.ContainsAny(v, "\r\n") {
		return NewValidationError("%s: no es una direccion valida", field)
	}
	addr, err := mail.ParseAddress(v)
	if err != nil || addr.Name != "" || addr.Address != v {
		return NewValidationError("%s: no es una direccion valida", field)
	}
	at := strings.LastIndexByte(v, '@')
	if at < 1 || !strings.Contains(v[at+1:], ".") {
		return NewValidationError("%s: no es una direccion valida", field)
	}
	return nil
}

func validateDisplayName(field, v string) error {
	if utf8.RuneCountInString(v) > MaxDisplayNameLength {
		return NewValidationError("%s: admite como maximo %d caracteres", field, MaxDisplayNameLength)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return NewValidationError("%s: contiene caracteres de control", field)
		}
	}
	return nil
}

// readyToSend es lo que se exige a una campana antes de salir de borrador.
func (c *Campaign) readyToSend() error {
	if err := c.validateContent(); err != nil {
		return err
	}
	if c.ABTest != nil {
		for i, v := range c.ABTest.Variants {
			if v.PinnedVersion == nil {
				return NewValidationError("ab_test: la variante %s no tiene versión fijada", VariantLabel(i))
			}
		}
	}
	return nil
}

// ValidateScheduleTime comprueba la fecha de programacion antes de consultar a nadie.
func ValidateScheduleTime(at, now time.Time) error {
	if !at.After(now.Add(MinScheduleLead)) {
		return ErrScheduleInPast
	}
	if at.After(now.Add(MaxScheduleHorizon)) {
		return ErrScheduleTooFar
	}
	return nil
}

func validVersion(version int) error {
	if version < 1 {
		return NewValidationError("template_version: debe ser mayor que cero")
	}
	return nil
}

// Editable dice si la campana admite un PATCH.
func (c *Campaign) Editable() bool {
	return c.Status == StatusDraft || c.Status == StatusPaused
}

// ContentLocked dice si plantilla y audiencia estan fijas: en pausa ya hay destinatarios
// que recibieron una version concreta de una plantilla concreta.
func (c *Campaign) ContentLocked() bool {
	return c.Status == StatusPaused
}

// CanPause, CanResume y CanCancel son las transiciones que admite el estado actual.
func (c *Campaign) CanPause() bool {
	return c.Status == StatusSending || c.Status == StatusScheduled
}

func (c *Campaign) CanResume() bool {
	return c.Status == StatusPaused
}

func (c *Campaign) CanCancel() bool {
	return c.Status == StatusScheduled || c.Status == StatusSending || c.Status == StatusPaused
}

// CanSchedule dice si la campana admite programarse (o reprogramarse) ahora.
func (c *Campaign) CanSchedule() bool {
	return c.Status == StatusDraft || c.Status == StatusScheduled
}

// CanStart dice si la campana admite iniciarse ya.
func (c *Campaign) CanStart() bool {
	return c.Status == StatusDraft || c.Status == StatusScheduled
}

// Schedule la programa para at y fija la version de la plantilla. Reprogramar una
// campana ya programada vuelve a fijar la version publicada del momento.
func (c *Campaign) Schedule(at time.Time, version int, now time.Time) error {
	if !c.CanSchedule() {
		return TransitionError(c.Status, StatusScheduled)
	}
	if err := ValidateScheduleTime(at, now); err != nil {
		return err
	}
	if err := validVersion(version); err != nil {
		return err
	}
	if err := c.readyToSend(); err != nil {
		return err
	}
	at = at.UTC()
	c.TimezoneDelivery = nil
	c.Status = StatusScheduled
	c.ScheduledAt = &at
	c.TemplateVersion = &version
	c.PauseReason = ""
	c.ResumeAfter = nil
	return nil
}

// ScheduleLocal la programa para que cada contacto la reciba a la hora de pared local en
// su zona (o en fallback). Arranca en el primer instante en que alguna zona marca esa
// hora, o en cuanto se pueda si ya paso: ahi el primer tramo envia a quien ya la alcanzo
// y da de alta los demas. La hora en la zona de respaldo debe quedar en el futuro.
func (c *Campaign) ScheduleLocal(local LocalDateTime, fallback string, version int, now time.Time) error {
	if !c.CanSchedule() {
		return TransitionError(c.Status, StatusScheduled)
	}
	if local.IsZero() {
		return NewValidationError("local_send_at: es obligatorio")
	}
	loc, err := LoadTimezone(fallback)
	if err != nil {
		return NewValidationError("fallback_timezone: %q no es una zona IANA válida (America/Lima)", strings.TrimSpace(fallback))
	}
	if err := ValidateScheduleTime(local.In(loc), now); err != nil {
		return err
	}
	if err := validVersion(version); err != nil {
		return err
	}
	if c.ABTest != nil {
		return ErrABWithTimezone
	}
	delivery := &TimezoneDelivery{LocalSendAt: local, FallbackTimezone: loc.String()}
	start := local.Earliest()
	if earliest := now.Add(MinScheduleLead); start.Before(earliest) {
		start = earliest
	}
	start = start.UTC()
	prev := c.TimezoneDelivery
	c.TimezoneDelivery = delivery
	if err := c.readyToSend(); err != nil {
		c.TimezoneDelivery = prev
		return err
	}
	c.Status = StatusScheduled
	c.ScheduledAt = &start
	c.TemplateVersion = &version
	c.PauseReason = ""
	c.ResumeAfter = nil
	return nil
}

// Start la pone en envio ahora mismo.
func (c *Campaign) Start(version int, now time.Time) error {
	if !c.CanStart() {
		return TransitionError(c.Status, StatusSending)
	}
	if err := validVersion(version); err != nil {
		return err
	}
	if err := c.readyToSend(); err != nil {
		return err
	}
	c.TimezoneDelivery = nil
	c.Status = StatusSending
	c.TemplateVersion = &version
	c.StartedAt = &now
	c.PauseReason = ""
	c.ResumeAfter = nil
	return nil
}

// Pause detiene una campana en envio o retiene una programada. El motivo queda para
// quien la reanude: una restriccion de reputacion no se levanta sola.
func (c *Campaign) Pause(reason string) error {
	if !c.CanPause() {
		return TransitionError(c.Status, StatusPaused)
	}
	c.Status = StatusPaused
	c.PauseReason = TruncateReason(reason)
	c.ResumeAfter = nil
	return nil
}

// Resume la devuelve a donde estaba: a programada si nunca empezo y su fecha sigue en
// el futuro, a envio en otro caso. firstStart indica que es la primera vez que envia.
func (c *Campaign) Resume(now time.Time) (firstStart bool, err error) {
	if !c.CanResume() {
		return false, TransitionError(c.Status, StatusSending)
	}
	c.PauseReason = ""
	c.ResumeAfter = nil
	if c.StartedAt == nil && c.ScheduledAt != nil && c.ScheduledAt.After(now) {
		c.Status = StatusScheduled
		return false, nil
	}
	c.Status = StatusSending
	if c.StartedAt == nil {
		c.StartedAt = &now
		return true, nil
	}
	return false, nil
}

func (c *Campaign) Cancel() error {
	if !c.CanCancel() {
		return TransitionError(c.Status, StatusCancelled)
	}
	c.Status = StatusCancelled
	c.ResumeAfter = nil
	return nil
}

// Complete cierra la campana cuando la audiencia se agoto y el ultimo lote se entrego.
func (c *Campaign) Complete(now time.Time) error {
	if c.Status != StatusSending {
		return TransitionError(c.Status, StatusCompleted)
	}
	c.Status = StatusCompleted
	c.CompletedAt = &now
	c.ResumeAfter = nil
	return nil
}

// Fail la cierra por un rechazo que reintentar no arregla (dominio sin verificar,
// plantilla que no es de marketing, audiencia que ya no existe).
func (c *Campaign) Fail(reason string) error {
	if c.Status != StatusSending {
		return TransitionError(c.Status, StatusFailed)
	}
	c.Status = StatusFailed
	c.FailureReason = TruncateReason(reason)
	c.ResumeAfter = nil
	return nil
}

func (c *Campaign) Deletable() bool {
	return c.Status == StatusDraft || c.Status == StatusCancelled
}

// TruncateReason recorta un motivo a MaxReasonLength caracteres sin partir runas.
func TruncateReason(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= MaxReasonLength {
		return s
	}
	return string([]rune(s)[:MaxReasonLength])
}
