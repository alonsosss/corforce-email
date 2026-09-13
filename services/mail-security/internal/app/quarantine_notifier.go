package app

import (
	"context"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// maxMessagesPerNotice acota los mensajes de un aviso: los mas recientes de cada buzon.
// Los que no caben quedan sin avisar y salen en el aviso siguiente.
const maxMessagesPerNotice = 100

// QuarantineNotifier envia a cada buzon el aviso de lo que tiene en cuarentena, por
// transactional, para las empresas con notify_enabled. Corre en segundo plano con un
// cerrojo de lider: en toda la celda solo una instancia barre a la vez.
type QuarantineNotifier struct {
	tx       ports.OwnerTransactor
	policy   ports.PolicyReader
	notices  ports.QuarantineNoticeRepository
	dir      ports.DirectoryReader
	sender   ports.NoticeSender
	links    *domain.QuarantineLinkSigner
	interval time.Duration
	logger   *zap.Logger
	now      func() time.Time
}

type NotifierDeps struct {
	Tx        ports.OwnerTransactor
	Policy    ports.PolicyReader
	Notices   ports.QuarantineNoticeRepository
	Directory ports.DirectoryReader
	Sender    ports.NoticeSender
	Links     *domain.QuarantineLinkSigner
	Interval  time.Duration
	Logger    *zap.Logger
}

func NewQuarantineNotifier(d NotifierDeps) *QuarantineNotifier {
	return &QuarantineNotifier{tx: d.Tx, policy: d.Policy, notices: d.Notices, dir: d.Directory, sender: d.Sender,
		links: d.Links, interval: d.Interval, logger: d.Logger, now: time.Now}
}

// SweepResult cuenta lo que hizo un barrido.
type SweepResult struct {
	Sent, Suppressed, Rejected, Skipped, Retry int
}

// Run barre al arrancar y cada intervalo, solo si consigue el cerrojo, y bloquea hasta que
// el contexto se cancele.
func (n *QuarantineNotifier) Run(ctx context.Context, acquire func(context.Context) (release func(), ok bool)) {
	ticker := time.NewTicker(n.interval)
	defer ticker.Stop()
	for {
		n.tick(ctx, acquire)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (n *QuarantineNotifier) tick(ctx context.Context, acquire func(context.Context) (func(), bool)) {
	release, ok := acquire(ctx)
	if !ok {
		return
	}
	defer release()
	tctx, cancel := context.WithTimeout(ctx, n.interval)
	defer cancel()
	res, err := n.Sweep(tctx)
	if err != nil && tctx.Err() == nil {
		n.logger.Warn("barrido del aviso de cuarentena incompleto", zap.Error(err))
	}
	if res != (SweepResult{}) {
		n.logger.Info("aviso de cuarentena", zap.Int("enviados", res.Sent), zap.Int("suprimidos", res.Suppressed),
			zap.Int("rechazados", res.Rejected), zap.Int("omitidos", res.Skipped), zap.Int("reintentar", res.Retry))
	}
	if pruned, err := n.notices.PruneHistory(tctx, domain.DefaultQuarantineMaxAgeDays); err != nil {
		n.logger.Warn("poda del historial de avisos y enlaces", zap.Error(err))
	} else if pruned > 0 {
		n.logger.Info("historial de avisos y enlaces podado", zap.Int64("filas", pruned))
	}
}

// Sweep es una pasada: por cada empresa con el aviso activo y bien configurado agrupa por
// buzon lo pendiente y envia un aviso por buzon. Un fallo de una empresa no para a las
// demas; el error devuelto es el primero que impidio leer los ajustes.
func (n *QuarantineNotifier) Sweep(ctx context.Context) (SweepResult, error) {
	var res SweepResult
	all, err := n.policy.AllQuarantineSettings(ctx)
	if err != nil {
		return res, err
	}
	for _, s := range all {
		if !s.Notify.Enabled {
			continue
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		n.sweepTenant(ctx, s, &res)
	}
	return res, nil
}

func (n *QuarantineNotifier) sweepTenant(ctx context.Context, s domain.QuarantineSettings, res *SweepResult) {
	log := n.logger.With(zap.String("tenant", s.TenantID.String()))
	// Unos ajustes guardados antes de validarse (o editados en la base) no se envian a
	// medias: se avisa en el log y se reintenta en cada barrido hasta que se corrijan.
	if err := domain.ValidateQuarantineNotify(s.Notify); err != nil {
		log.Warn("aviso de cuarentena activo pero mal configurado; no se envia", zap.Error(err))
		return
	}
	tpl, err := domain.ParseNoticeTemplate(s.Notify.HTMLTemplate)
	if err != nil {
		log.Warn("plantilla del aviso de cuarentena", zap.Error(err))
		return
	}
	pending, err := n.notices.PendingNotices(ctx, s.TenantID, s.Notify.MaxScore, maxMessagesPerNotice)
	if err != nil {
		log.Warn("cuarentena pendiente de aviso", zap.Error(err))
		return
	}
	own, rest := domain.SplitOwnNotices(pending, s.Notify.Sender)
	for _, g := range domain.GroupByMailbox(own) {
		if ctx.Err() != nil {
			return
		}
		record := &domain.QuarantineNotice{
			ID: uuid.New(), TenantID: s.TenantID, Rcpt: g.Mailbox, IdempotencyKey: domain.NoticeIdempotencyKey(g.Mailbox, g.Latest().ID),
			QuarantineIDs: g.IDs(), CreatedAt: n.now(), Status: domain.NoticeSkipped, ErrorCode: domain.NoticeOwnNoticeCode,
		}
		if n.save(ctx, record, log.With(zap.String("rcpt", g.Mailbox))) == domain.NoticeSkipped {
			res.Skipped++
		} else {
			res.Retry++
		}
	}
	for _, g := range domain.GroupByMailbox(rest) {
		if ctx.Err() != nil {
			return
		}
		switch n.notify(ctx, s, tpl, g, log) {
		case domain.NoticeSent:
			res.Sent++
		case domain.NoticeSuppressed:
			res.Suppressed++
		case domain.NoticeRejected:
			res.Rejected++
		case domain.NoticeSkipped:
			res.Skipped++
		default:
			res.Retry++
		}
	}
}

// notify envia el aviso de un buzon y devuelve como termino; "" si hay que reintentar en el
// siguiente barrido (transactional o la base no respondieron).
func (n *QuarantineNotifier) notify(ctx context.Context, s domain.QuarantineSettings, tpl *domain.NoticeTemplate, g domain.NoticeGroup, log *zap.Logger) domain.NoticeStatus {
	log = log.With(zap.String("rcpt", g.Mailbox))
	key := domain.NoticeIdempotencyKey(g.Mailbox, g.Latest().ID)
	record := &domain.QuarantineNotice{
		ID: uuid.New(), TenantID: s.TenantID, Rcpt: g.Mailbox, IdempotencyKey: key, QuarantineIDs: g.IDs(), CreatedAt: n.now(),
	}

	// Un buzon dado de baja, desactivado o convertido en recurso no recibe el aviso: enviarlo
	// por SES a una direccion propia que no existe acabaria en un rebote duro.
	mb, err := n.dir.MailboxByUsername(ctx, g.Mailbox)
	switch {
	case errors.Is(err, domain.ErrNotFound) || (err == nil && (mb.TenantID != s.TenantID || !mb.Receives())):
		record.Status = domain.NoticeSkipped
		return n.save(ctx, record, log)
	case err != nil:
		log.Warn("buzon del aviso de cuarentena", zap.Error(err))
		return ""
	}

	expires := n.now().Add(n.links.TTL())
	data := domain.NewNoticeData(g, expires, func(m domain.QuarantineItem, a domain.QuarantineLinkAction) string {
		return n.links.URL(domain.QuarantineLinkClaims{TenantID: s.TenantID, MessageID: m.ID, Action: a, ExpiresAt: expires.Unix()}, m.QHash)
	})
	html, err := tpl.Render(data)
	if err != nil {
		log.Warn("aviso de cuarentena no renderizado; se da por atendido", zap.Error(err))
		record.Status, record.ErrorCode = domain.NoticeRejected, "TEMPLATE_RENDER_FAILED"
		return n.save(ctx, record, log)
	}

	receipt, err := n.sender.SendQuarantineNotice(ctx, s.TenantID, domain.NoticeMail{
		From: s.Notify.Sender, To: g.Mailbox, Subject: s.Notify.Subject, HTML: html, IdempotencyKey: key,
	})
	var rejected *ports.NoticeRejectedError
	switch {
	case err == nil:
		record.Status, record.MessageID = domain.NoticeSent, receipt.MessageID
		if receipt.Suppressed {
			record.Status = domain.NoticeSuppressed
		}
	case errors.As(err, &rejected):
		log.Warn("transactional rechazo el aviso de cuarentena; no se reintenta",
			zap.Int("status", rejected.Status), zap.String("code", rejected.Code), zap.String("message", rejected.Message))
		record.Status, record.ErrorCode = domain.NoticeRejected, rejected.Code
	default:
		log.Warn("aviso de cuarentena no enviado; se reintenta en el siguiente barrido", zap.Error(err))
		return ""
	}
	return n.save(ctx, record, log)
}

// save marca los mensajes como avisados y registra el aviso en UNA transaccion: o quedan
// las dos cosas o ninguna, y en ese caso el siguiente barrido repite con la misma clave y
// transactional devuelve lo que ya creo.
func (n *QuarantineNotifier) save(ctx context.Context, record *domain.QuarantineNotice, log *zap.Logger) domain.NoticeStatus {
	err := n.tx.Transact(ctx, func(ctx context.Context) error {
		if err := n.notices.MarkNotified(ctx, record.TenantID, record.QuarantineIDs); err != nil {
			return err
		}
		return n.notices.InsertNotice(ctx, record)
	})
	if err != nil {
		log.Warn("aviso de cuarentena sin registrar; se repite en el siguiente barrido",
			zap.String("status", string(record.Status)), zap.Error(err))
		return ""
	}
	return record.Status
}
