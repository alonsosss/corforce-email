package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Status es el ciclo de vida de un flujo: draft -> active <-> paused, y archived desde
// cualquiera de los tres. Solo se edita en draft o paused.
type Status string

const (
	StatusDraft    Status = "draft"
	StatusActive   Status = "active"
	StatusPaused   Status = "paused"
	StatusArchived Status = "archived"
)

func Statuses() []Status { return []Status{StatusDraft, StatusActive, StatusPaused, StatusArchived} }

// ParseStatus valida un estado recibido por la API.
func ParseStatus(s string) (Status, bool) {
	for _, st := range Statuses() {
		if string(st) == s {
			return st, true
		}
	}
	return "", false
}

// TriggerType es la lista blanca de disparadores. Cada uno corresponde a un evento real
// de su productor; el adaptador de NATS hace la correspondencia.
type TriggerType string

const (
	// TriggerContactCreated: alta de un contacto (contacts).
	TriggerContactCreated TriggerType = "contact.created"
	// TriggerConsentGranted: concesion del consentimiento de marketing (contacts).
	TriggerConsentGranted TriggerType = "consent.granted"
	// TriggerEmailClicked: clic en un correo de marketing con contacto (transactional).
	TriggerEmailClicked TriggerType = "email.clicked"
	// TriggerContactDate: aniversario (dia y mes) de un atributo de fecha del contacto, a
	// una hora local, en la zona del contacto o en la de respaldo. No llega por un evento:
	// lo busca el ejecutor en contacts (ScanDateTriggers).
	TriggerContactDate TriggerType = "contact.date"
)

func TriggerTypes() []TriggerType {
	return []TriggerType{TriggerContactCreated, TriggerConsentGranted, TriggerEmailClicked, TriggerContactDate}
}

// AcceptsCampaign dice si el disparador admite acotarse a una campana (CampaignID).
func (t TriggerType) AcceptsCampaign() bool { return t == TriggerEmailClicked }

// IsDate dice si el disparador es un aniversario (Attribute, Hour y Timezone).
func (t TriggerType) IsDate() bool { return t == TriggerContactDate }

// MaxTriggerHour es la ultima hora local de un disparador por fecha (0..23).
const MaxTriggerHour = 23

// PurposeMarketing es el unico proposito de consentimiento que dispara un flujo.
const PurposeMarketing = "marketing"

// Trigger es el disparador de un flujo. CampaignID solo aplica a email.clicked y acota
// los clics a los de una campana concreta. Attribute (clave de un atributo de fecha de
// contacts), Hour (hora local) y Timezone (zona IANA de respaldo para quien no tiene la
// suya) solo aplican a contact.date, y los tres son obligatorios.
type Trigger struct {
	Type       TriggerType `json:"type"`
	CampaignID *uuid.UUID  `json:"campaign_id,omitempty"`
	Attribute  string      `json:"attribute,omitempty"`
	Hour       *int        `json:"hour,omitempty"`
	Timezone   string      `json:"timezone,omitempty"`
}

func (t *Trigger) validate() error {
	known := false
	for _, tt := range TriggerTypes() {
		known = known || t.Type == tt
	}
	if !known {
		return NewValidationError("trigger.type debe ser contact.created, consent.granted, email.clicked o contact.date")
	}
	t.Attribute = strings.TrimSpace(t.Attribute)
	t.Timezone = strings.TrimSpace(t.Timezone)
	if !t.Type.IsDate() {
		if t.Attribute != "" || t.Hour != nil || t.Timezone != "" {
			return NewValidationError("trigger.attribute, trigger.hour y trigger.timezone solo aplican a contact.date")
		}
	} else {
		if !attributeKeyPattern.MatchString(t.Attribute) {
			return NewValidationError("trigger.attribute debe ser la clave de un atributo de fecha declarado")
		}
		if t.Hour == nil || *t.Hour < 0 || *t.Hour > MaxTriggerHour {
			return NewValidationError("trigger.hour es obligatoria y va de 0 a %d (hora local)", MaxTriggerHour)
		}
		if _, err := LoadTimezone(t.Timezone); err != nil {
			return err
		}
	}
	if t.CampaignID != nil {
		if !t.Type.AcceptsCampaign() {
			return NewValidationError("trigger.campaign_id solo aplica a email.clicked")
		}
		if *t.CampaignID == uuid.Nil {
			return NewValidationError("trigger.campaign_id no es válido")
		}
	}
	return nil
}

// LoadTimezone valida la zona de respaldo de un disparador por fecha: IANA, sin "Local"
// (la del servidor no dice nada de la empresa).
func LoadTimezone(name string) (*time.Location, error) {
	if name == "" || name == "Local" || len(name) > 64 {
		return nil, NewValidationError("trigger.timezone debe ser una zona IANA (America/Lima)")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, NewValidationError("trigger.timezone debe ser una zona IANA (America/Lima)")
	}
	return loc, nil
}

// TriggerEvent es un evento de disparo ya interpretado por el adaptador.
type TriggerEvent struct {
	EventID    string
	TenantID   uuid.UUID
	Type       TriggerType
	ContactID  uuid.UUID
	CampaignID *uuid.UUID
}

type Workflow struct {
	ID          uuid.UUID  `json:"id"`
	TenantID    uuid.UUID  `json:"tenant_id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Status      Status     `json:"status"`
	Trigger     Trigger    `json:"trigger"`
	ListID      *uuid.UUID `json:"list_id"`
	ReEntry     bool       `json:"re_entry"`
	Steps       []Step     `json:"steps"`
	PauseReason string     `json:"pause_reason"`
	CreatedBy   uuid.UUID  `json:"created_by"`
	ActivatedAt *time.Time `json:"activated_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type NewWorkflowInput struct {
	Name        string
	Description string
	Trigger     Trigger
	ListID      *uuid.UUID
	ReEntry     bool
	Steps       []Step
	CreatedBy   uuid.UUID
}

const (
	MaxDescriptionLen = 2000
	// MaxPauseReasonLen acota el motivo que escribe quien pausa un flujo.
	MaxPauseReasonLen = 500
	// PauseReasonManual es el motivo de una pausa pedida sin texto. Las pausas del
	// ejecutor llevan "CODIGO: detalle" (BlockingCodes).
	PauseReasonManual = "manual"
)

// NewWorkflow crea un flujo en borrador con todo validado.
func NewWorkflow(tenantID uuid.UUID, in NewWorkflowInput) (*Workflow, error) {
	w := &Workflow{
		ID: uuid.New(), TenantID: tenantID, Status: StatusDraft, Name: in.Name, Description: in.Description,
		Trigger: in.Trigger, ListID: in.ListID, ReEntry: in.ReEntry, Steps: cloneSteps(in.Steps), CreatedBy: in.CreatedBy,
	}
	if in.CreatedBy == uuid.Nil {
		return nil, NewValidationError("created_by es obligatorio")
	}
	if err := w.validate(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Workflow) validate() error {
	w.Name = strings.TrimSpace(w.Name)
	if w.Name == "" || utf8.RuneCountInString(w.Name) > MaxNameLen {
		return NewValidationError("name es obligatorio y admite como máximo %d caracteres", MaxNameLen)
	}
	w.Description = strings.TrimSpace(w.Description)
	if utf8.RuneCountInString(w.Description) > MaxDescriptionLen {
		return NewValidationError("description admite como máximo %d caracteres", MaxDescriptionLen)
	}
	if err := w.Trigger.validate(); err != nil {
		return err
	}
	if w.ListID != nil && *w.ListID == uuid.Nil {
		return NewValidationError("list_id no es válido")
	}
	return ValidateSteps(w.Steps)
}

// Patch es una edicion parcial. SetListID distingue "quitar el filtro" (ListID nil) de
// "no tocarlo".
type Patch struct {
	Name        *string
	Description *string
	Trigger     *Trigger
	SetListID   bool
	ListID      *uuid.UUID
	ReEntry     *bool
	Steps       []Step
}

func (p Patch) empty() bool {
	return p.Name == nil && p.Description == nil && p.Trigger == nil && !p.SetListID && p.ReEntry == nil && p.Steps == nil
}

// Editable: la definicion solo cambia sin nadie recorriendola (borrador) o congelada (pausa).
func (w *Workflow) Editable() bool { return w.Status == StatusDraft || w.Status == StatusPaused }

func (w *Workflow) ApplyPatch(p Patch) error {
	if !w.Editable() {
		return ErrNotEditable
	}
	if p.empty() {
		return ErrNothingToUpdate
	}
	next := *w
	next.Steps = cloneSteps(w.Steps)
	if p.Name != nil {
		next.Name = *p.Name
	}
	if p.Description != nil {
		next.Description = *p.Description
	}
	if p.Trigger != nil {
		next.Trigger = *p.Trigger
	}
	if p.SetListID {
		next.ListID = p.ListID
	}
	if p.ReEntry != nil {
		next.ReEntry = *p.ReEntry
	}
	if p.Steps != nil {
		next.Steps = cloneSteps(p.Steps)
	}
	if err := next.validate(); err != nil {
		return err
	}
	*w = next
	return nil
}

// CanActivate, CanPause y CanArchive son las transiciones que admite el estado actual.
func (w *Workflow) CanActivate() bool { return w.Status == StatusDraft || w.Status == StatusPaused }
func (w *Workflow) CanPause() bool    { return w.Status == StatusActive }
func (w *Workflow) CanArchive() bool  { return w.Status != StatusArchived }

// Activate pone el flujo en marcha. Todos los pasos de envio deben llevar ya la version
// de plantilla fijada (la fija el caso de uso tras comprobarla en templates).
func (w *Workflow) Activate(now time.Time) error {
	if !w.CanActivate() {
		return TransitionError(w.Status, StatusActive)
	}
	if err := ValidateSteps(w.Steps); err != nil {
		return err
	}
	for i, s := range w.Steps {
		if s.Type == StepSendEmail && s.TemplateVersion == nil {
			return NewValidationError("steps[%d].template_version no está fijada", i)
		}
	}
	w.Status = StatusActive
	w.PauseReason = ""
	t := now
	w.ActivatedAt = &t
	return nil
}

// Pause congela el flujo: sus ejecuciones dejan de avanzar hasta reactivarlo.
func (w *Workflow) Pause(reason string) error {
	if !w.CanPause() {
		return TransitionError(w.Status, StatusPaused)
	}
	w.Status = StatusPaused
	w.PauseReason = TruncateReason(reason)
	return nil
}

// Archive retira el flujo; sus ejecuciones pendientes se cancelan.
func (w *Workflow) Archive() error {
	if !w.CanArchive() {
		return TransitionError(w.Status, StatusArchived)
	}
	w.Status = StatusArchived
	return nil
}

func (w *Workflow) Deletable() bool { return w.Status == StatusDraft || w.Status == StatusArchived }

// Accepts dice si el evento hace entrar a un contacto en el flujo, sin mirar la lista (eso
// lo resuelve contacts). Un clic en un correo del propio flujo no lo vuelve a disparar:
// sin esa regla, un flujo de clic sin campana concreta se realimentaria a si mismo.
func (w *Workflow) Accepts(ev TriggerEvent) bool {
	if w.Status != StatusActive || w.Trigger.Type != ev.Type || ev.ContactID == uuid.Nil || ev.Type.IsDate() {
		return false
	}
	if ev.Type != TriggerEmailClicked {
		return true
	}
	if ev.CampaignID != nil && *ev.CampaignID == w.ID {
		return false
	}
	if w.Trigger.CampaignID == nil {
		return true
	}
	return ev.CampaignID != nil && *ev.CampaignID == *w.Trigger.CampaignID
}
