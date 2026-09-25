package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"go.uber.org/zap"
)

// maxSettingsCacheEntries acota la cache del ajuste por buzon: al llenarse se vacia entera, que solo
// cuesta una consulta a mail-directory por buzon activo.
const maxSettingsCacheEntries = 10000

// AssistantConfig son los topes del asistente y cuanto se fia el webmail del ajuste de la empresa.
type AssistantConfig struct {
	Limits domain.AssistantLimits
	// SettingsTTL es lo que tarda en notarse que una empresa apago el asistente.
	SettingsTTL time.Duration
}

// AssistantDeps son las dependencias del asistente. Provider nil es una plataforma sin clave del
// proveedor: el asistente queda no disponible y el resto del webmail funciona igual.
type AssistantDeps struct {
	Provider ports.AssistantProvider
	Settings ports.AssistantSettings
	Quota    ports.AssistantQuota
	Audit    ports.AssistantAudit
	Metrics  ports.AssistantMetrics
	Source   ports.AssistantSource
	Clock    func() time.Time
	Logger   *zap.Logger
	Config   AssistantConfig
}

// AssistantService es el caso de uso del asistente (docs/adr/0015). Nada de lo que procesa se guarda:
// el texto va y vuelve en la misma peticion y solo queda constancia de quien, que y cuanto.
type AssistantService struct {
	provider ports.AssistantProvider
	settings ports.AssistantSettings
	quota    ports.AssistantQuota
	audit    ports.AssistantAudit
	metrics  ports.AssistantMetrics
	source   ports.AssistantSource
	clock    func() time.Time
	logger   *zap.Logger
	cfg      AssistantConfig

	mu    sync.Mutex
	cache map[string]settingsEntry
}

type settingsEntry struct {
	enabled bool
	expires time.Time
}

// NewAssistantService valida el cableado. Sin proveedor no hacen falta los demas puertos salvo la
// fuente y el registro, que se usan igual para responder que no esta disponible.
func NewAssistantService(d AssistantDeps) (*AssistantService, error) {
	if d.Settings == nil || d.Quota == nil || d.Audit == nil || d.Metrics == nil || d.Source == nil || d.Logger == nil {
		return nil, errors.New("webmail: faltan dependencias del asistente")
	}
	if err := d.Config.Limits.Validate(); err != nil {
		return nil, err
	}
	if d.Config.SettingsTTL <= 0 {
		return nil, errors.New("webmail: la cache del ajuste del asistente debe ser positiva")
	}
	clock := d.Clock
	if clock == nil {
		clock = time.Now
	}
	return &AssistantService{
		provider: d.Provider, settings: d.Settings, quota: d.Quota, audit: d.Audit, metrics: d.Metrics,
		source: d.Source, clock: clock, logger: d.Logger, cfg: d.Config, cache: map[string]settingsEntry{},
	}, nil
}

// Limits son los topes que aplica el asistente.
func (a *AssistantService) Limits() domain.AssistantLimits { return a.cfg.Limits }

// AssistantStatus es lo que la interfaz necesita para ofrecerlo o no.
type AssistantStatus struct {
	Available bool
	// Reason explica por que no esta disponible: not_configured (la plataforma no tiene clave) o
	// disabled (la empresa no lo activo). Vacio si esta disponible.
	Reason string
	Limits domain.AssistantLimits
	Usage  domain.AssistantUsage
}

const (
	assistantReasonNotConfigured = "not_configured"
	assistantReasonDisabled      = "disabled"
)

// Status dice si el asistente esta disponible para la sesion y cuanto se ha usado hoy.
func (a *AssistantService) Status(ctx context.Context, sess domain.Session) (AssistantStatus, error) {
	st := AssistantStatus{Limits: a.cfg.Limits}
	if a.provider == nil {
		st.Reason = assistantReasonNotConfigured
		return st, nil
	}
	// Una sesion anterior a que mail-auth devolviera empresa y buzon no lo ofrece; consultar el estado no
	// la cierra (usarlo si, y el usuario vuelve a entrar).
	mb, ok := sess.Mailbox()
	if !ok {
		st.Reason = assistantReasonDisabled
		return st, nil
	}
	enabled, err := a.enabled(ctx, sess.Username)
	if err != nil {
		return st, err
	}
	if !enabled {
		st.Reason = assistantReasonDisabled
		return st, nil
	}
	usage, err := a.quota.Usage(ctx, mb.TenantID, sess.Username, a.clock())
	if err != nil {
		a.logger.Warn("webmail: no se pudo leer el uso del asistente", zap.String("username", sess.Username), zap.Error(err))
		return st, unavailable(err)
	}
	st.Available, st.Usage = true, usage
	return st, nil
}

// Summarize resume uno o varios mensajes de un hilo del buzon.
func (a *AssistantService) Summarize(ctx context.Context, sess domain.Session, refs []domain.MessageRef) (domain.AssistantResult, error) {
	if len(refs) == 0 || len(refs) > a.cfg.Limits.MaxThreadMessages {
		return domain.AssistantResult{}, domain.NewValidationError("messages", fmt.Sprintf("indica entre 1 y %d mensajes", a.cfg.Limits.MaxThreadMessages))
	}
	return a.text(ctx, sess, domain.AssistantSummarize, len(refs), func() (domain.AssistantPrompt, error) {
		msgs, err := a.source.AssistantMessages(ctx, sess, refs)
		if err != nil {
			return domain.AssistantPrompt{}, err
		}
		return domain.BuildSummaryPrompt(msgs, a.cfg.Limits)
	})
}

// Reply propone el cuerpo de una respuesta al mensaje, con las indicaciones opcionales del usuario.
func (a *AssistantService) Reply(ctx context.Context, sess domain.Session, ref domain.MessageRef, instructions string) (domain.AssistantResult, error) {
	return a.text(ctx, sess, domain.AssistantReply, 1, func() (domain.AssistantPrompt, error) {
		msg, err := a.one(ctx, sess, ref)
		if err != nil {
			return domain.AssistantPrompt{}, err
		}
		return domain.BuildReplyPrompt(msg, instructions, sess.DisplayName, a.cfg.Limits)
	})
}

// Tone reescribe el borrador del usuario con otro tono. El borrador lo manda el cliente: es texto del
// propio usuario, no del buzon.
func (a *AssistantService) Tone(ctx context.Context, sess domain.Session, draft string, tone domain.AssistantToneName) (domain.AssistantResult, error) {
	return a.text(ctx, sess, domain.AssistantTone, 0, func() (domain.AssistantPrompt, error) {
		return domain.BuildTonePrompt(draft, tone, a.cfg.Limits)
	})
}

// Extract propone las tareas y las citas con fecha del mensaje. Crear un evento es otra peticion que
// el usuario confirma (calendario del buzon); aqui no se crea nada.
func (a *AssistantService) Extract(ctx context.Context, sess domain.Session, ref domain.MessageRef, today string) (domain.Extraction, bool, error) {
	if today == "" {
		today = a.clock().UTC().Format("2006-01-02")
	}
	comp, prompt, err := a.run(ctx, sess, domain.AssistantExtract, 1, func() (domain.AssistantPrompt, error) {
		msg, err := a.one(ctx, sess, ref)
		if err != nil {
			return domain.AssistantPrompt{}, err
		}
		return domain.BuildExtractPrompt(msg, today, a.cfg.Limits)
	}, func(c domain.AssistantCompletion) (int, error) {
		if c.Truncated {
			return 0, domain.ErrAssistantEmptyResult
		}
		x, err := domain.ParseExtraction(c.Text)
		return len(x.Tasks) + len(x.Events), err
	})
	if err != nil {
		return domain.Extraction{}, false, err
	}
	x, err := domain.ParseExtraction(comp.Text)
	return x, prompt.InputTruncated, err
}

func (a *AssistantService) one(ctx context.Context, sess domain.Session, ref domain.MessageRef) (domain.AssistantSourceMessage, error) {
	msgs, err := a.source.AssistantMessages(ctx, sess, []domain.MessageRef{ref})
	if err != nil {
		return domain.AssistantSourceMessage{}, err
	}
	if len(msgs) != 1 {
		return domain.AssistantSourceMessage{}, domain.ErrMessageNotFound
	}
	return msgs[0], nil
}

// text corre una accion cuyo resultado es texto propuesto.
func (a *AssistantService) text(ctx context.Context, sess domain.Session, action domain.AssistantAction, messages int, build func() (domain.AssistantPrompt, error)) (domain.AssistantResult, error) {
	var text string
	comp, prompt, err := a.run(ctx, sess, action, messages, build, func(c domain.AssistantCompletion) (int, error) {
		text = strings.TrimSpace(c.Text)
		if text == "" {
			return 0, domain.ErrAssistantEmptyResult
		}
		return utf8.RuneCountInString(text), nil
	})
	if err != nil {
		return domain.AssistantResult{}, err
	}
	return domain.AssistantResult{Text: text, InputTruncated: prompt.InputTruncated, OutputTruncated: comp.Truncated}, nil
}

// run es el camino comun: disponibilidad, ajuste de la empresa, lectura y composicion (que validan la
// entrada antes de gastar cupo), cupo diario, proveedor, y metricas y auditoria del resultado. accept
// valida la salida y devuelve su tamano para la auditoria.
func (a *AssistantService) run(ctx context.Context, sess domain.Session, action domain.AssistantAction, messages int,
	build func() (domain.AssistantPrompt, error), accept func(domain.AssistantCompletion) (int, error),
) (domain.AssistantCompletion, domain.AssistantPrompt, error) {
	var none domain.AssistantCompletion
	if a.provider == nil {
		return none, domain.AssistantPrompt{}, domain.ErrAssistantNotConfigured
	}
	mb, ok := sess.Mailbox()
	if !ok {
		return none, domain.AssistantPrompt{}, domain.ErrSessionInvalid
	}
	enabled, err := a.enabled(ctx, sess.Username)
	if err != nil {
		return none, domain.AssistantPrompt{}, err
	}
	if !enabled {
		a.metrics.AssistantRequest(action, domain.AssistantOutcomeDisabled)
		return none, domain.AssistantPrompt{}, domain.ErrAssistantDisabled
	}
	// Con el cupo ya agotado no se lee el buzon: un resumen carga hasta MaxThreadMessages
	// mensajes. El cupo se consume despues de validar la entrada, como siempre.
	if usage, err := a.quota.Usage(ctx, mb.TenantID, sess.Username, a.clock()); err == nil {
		if qerr := a.cfg.Limits.Exhausted(usage); qerr != nil {
			a.metrics.AssistantRequest(action, domain.AssistantOutcomeQuota)
			return none, domain.AssistantPrompt{}, qerr
		}
	}
	prompt, err := build()
	if err != nil {
		return none, prompt, err
	}
	now := a.clock()
	if _, err := a.quota.Consume(ctx, mb.TenantID, sess.Username, now, a.cfg.Limits); err != nil {
		var qerr *domain.AssistantQuotaError
		if errors.As(err, &qerr) {
			a.metrics.AssistantRequest(action, domain.AssistantOutcomeQuota)
			return none, prompt, err
		}
		a.logger.Warn("webmail: no se pudo anotar el uso del asistente", zap.String("username", sess.Username), zap.Error(err))
		return none, prompt, unavailable(err)
	}

	rec := domain.AssistantUsageRecord{
		TenantID: mb.TenantID, MailboxID: mb.MailboxID, Username: sess.Username, Action: action,
		InputChars: prompt.InputChars, Messages: messages, At: now.UTC(),
	}
	comp, err := a.provider.Complete(ctx, prompt)
	a.metrics.AssistantLatency(action, a.clock().Sub(now))
	rec.Model, rec.InputTokens, rec.OutputTokens = comp.Model, comp.InputTokens, comp.OutputTokens
	if comp.Model != "" {
		a.metrics.AssistantTokens(comp.Model, comp.InputTokens, comp.OutputTokens)
	}
	if err == nil {
		rec.OutputChars, err = accept(comp)
	}
	rec.Outcome = assistantOutcome(err)
	a.metrics.AssistantRequest(action, rec.Outcome)
	if aerr := a.audit.AssistantUsed(ctx, rec); aerr != nil {
		a.metrics.AssistantAuditFailed()
		a.logger.Error("webmail: uso del asistente sin apunte de auditoria", zap.String("username", sess.Username),
			zap.String("action", string(action)), zap.Error(aerr))
	}
	if err != nil {
		if rec.Outcome == domain.AssistantOutcomeFailed || rec.Outcome == domain.AssistantOutcomeBusy {
			a.logger.Warn("webmail: el proveedor del asistente fallo", zap.String("action", string(action)), zap.Error(err))
		}
		return none, prompt, err
	}
	return comp, prompt, nil
}

func assistantOutcome(err error) string {
	switch {
	case err == nil:
		return domain.AssistantOutcomeOK
	case errors.Is(err, domain.ErrAssistantBusy):
		return domain.AssistantOutcomeBusy
	case errors.Is(err, domain.ErrAssistantRefused):
		return domain.AssistantOutcomeRefused
	case errors.Is(err, domain.ErrAssistantEmptyResult):
		return domain.AssistantOutcomeEmpty
	default:
		return domain.AssistantOutcomeFailed
	}
}

// enabled consulta el ajuste de la empresa del buzon con una cache corta. Un fallo del directorio no
// se cachea y deja el asistente sin servir: ante la duda, el correo no sale.
func (a *AssistantService) enabled(ctx context.Context, username string) (bool, error) {
	now := a.clock()
	a.mu.Lock()
	if e, ok := a.cache[username]; ok && now.Before(e.expires) {
		a.mu.Unlock()
		return e.enabled, nil
	}
	a.mu.Unlock()
	enabled, err := a.settings.AssistantEnabled(ctx, username)
	if err != nil {
		a.logger.Warn("webmail: no se pudo leer el ajuste del asistente", zap.String("username", username), zap.Error(err))
		return false, unavailable(err)
	}
	a.mu.Lock()
	if len(a.cache) >= maxSettingsCacheEntries {
		a.cache = map[string]settingsEntry{}
	}
	a.cache[username] = settingsEntry{enabled: enabled, expires: now.Add(a.cfg.SettingsTTL)}
	a.mu.Unlock()
	return enabled, nil
}

// AssistantMessages lee los mensajes para el asistente con una sola conexion y sin marcarlos como
// leidos. Del mensaje solo sale el remitente visible, la fecha, el asunto y el cuerpo en texto plano (el
// HTML se pasa a texto con el saneado de salida); nunca adjuntos ni el resto de cabeceras.
func (s *Service) AssistantMessages(ctx context.Context, sess domain.Session, refs []domain.MessageRef) ([]domain.AssistantSourceMessage, error) {
	for _, ref := range refs {
		if err := domain.ValidateFolderName(ref.Folder); err != nil {
			return nil, err
		}
		if ref.UID == 0 {
			return nil, domain.NewValidationError("uid", "identificador de mensaje inválido")
		}
	}
	out := make([]domain.AssistantSourceMessage, 0, len(refs))
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		for _, ref := range refs {
			raw, err := mb.Read(ctx, ref.Folder, ref.UID, domain.ReadOptions{MarkSeen: false, MaxBodyBytes: s.cfg.MaxBodyPartBytes})
			if err != nil {
				return err
			}
			body, truncated := raw.Text, raw.TextTruncated
			if strings.TrimSpace(body) == "" && raw.HTML != "" {
				_, body = s.sanitizer.Outgoing(raw.HTML)
				truncated = raw.HTMLTruncated
			}
			out = append(out, domain.NewAssistantSource(raw.Envelope, body, truncated))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
