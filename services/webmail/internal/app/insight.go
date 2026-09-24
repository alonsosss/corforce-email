package app

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	"go.uber.org/zap"
)

const (
	// colleagueSearchLimit acota los companeros que se comparan con el nombre visible del remitente.
	colleagueSearchLimit = 10
	// domainSearchLimit basta para saber si un dominio tiene buzones en la empresa.
	domainSearchLimit = 5
)

// ListThreads devuelve una pagina de conversaciones de la carpeta.
func (s *Service) ListThreads(ctx context.Context, sess domain.Session, folder string, q domain.ListQuery) (domain.ThreadPage, error) {
	if err := domain.ValidateFolderName(folder); err != nil {
		return domain.ThreadPage{}, err
	}
	var page domain.ThreadPage
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		var err error
		page, err = mb.ListThreads(ctx, folder, q)
		return err
	})
	return page, err
}

// Conversation abre la conversacion de un mensaje: los de la carpeta y, si la carpeta no es
// Enviados, las respuestas propias guardadas alli. Va de la mas antigua a la mas reciente, acotada
// a las MaxThreadMessages mas recientes.
func (s *Service) Conversation(ctx context.Context, sess domain.Session, folder string, uid uint32) ([]domain.ConversationMessage, error) {
	if err := domain.ValidateFolderName(folder); err != nil {
		return nil, err
	}
	var out []domain.ConversationMessage
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		msgs, err := mb.Conversation(ctx, folder, uid, domain.MaxThreadMessages)
		if err != nil {
			return err
		}
		out = msgs
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		sent, ok := domain.FolderWithRole(folders, domain.RoleSent)
		if !ok || sent.Name == folder {
			return nil
		}
		ids := relatedMessageIDs(msgs)
		related, err := mb.Related(ctx, sent.Name, ids, domain.MaxThreadMessages)
		if err != nil {
			// Sin Enviados la conversacion se muestra igual con lo recibido.
			s.logger.Warn("webmail: no se pudieron leer las respuestas propias de una conversacion",
				zap.String("username", sess.Username), zap.Error(err))
			return nil
		}
		out = append(out, related...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return orderConversation(out), nil
}

// relatedMessageIDs son los identificadores con los que se buscan en Enviados los mensajes propios de la
// conversacion: los de sus mensajes, para las respuestas propias, y aquellos a los que responden, para el
// mensaje propio que la abrio (la respuesta recibida lo cita en In-Reply-To). Sin repetidos y acotados.
func relatedMessageIDs(msgs []domain.ConversationMessage) []string {
	var ids []string
	seen := map[string]bool{}
	add := func(id string) {
		key := strings.ToLower(id)
		if id == "" || seen[key] || len(ids) >= domain.MaxRelatedMessageIDs {
			return
		}
		seen[key] = true
		ids = append(ids, id)
	}
	for _, m := range msgs {
		add(m.MessageID)
	}
	for _, m := range msgs {
		for _, id := range m.InReplyTo {
			add(id)
		}
	}
	return ids
}

// orderConversation quita repetidos y deja los MaxThreadMessages mas recientes, del mas antiguo
// al mas reciente.
func orderConversation(msgs []domain.ConversationMessage) []domain.ConversationMessage {
	seen := map[string]bool{}
	out := make([]domain.ConversationMessage, 0, len(msgs))
	for _, m := range msgs {
		key := m.Folder + "\x00" + m.MessageID
		if m.MessageID == "" {
			key = m.Folder + "\x00uid:" + strconv.FormatUint(uint64(m.UID), 10)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	if len(out) > domain.MaxThreadMessages {
		out = out[len(out)-domain.MaxThreadMessages:]
	}
	return out
}

// SenderInsight arma la ficha de un mensaje: pestana, escudo antifraude y baja. Lo que el directorio
// de la empresa no pueda responder deja el escudo como parcial, nunca impide leer la ficha.
func (s *Service) SenderInsight(ctx context.Context, sess domain.Session, folder string, uid uint32) (domain.SenderInsight, error) {
	if err := domain.ValidateFolderName(folder); err != nil {
		return domain.SenderInsight{}, err
	}
	src, role, err := s.insightSource(ctx, sess, folder, uid)
	if err != nil {
		return domain.SenderInsight{}, err
	}
	in := domain.ShieldInput{From: src.From, ReplyTo: src.ReplyTo, Headers: src.Headers}
	in.OwnDomains, in.Colleagues, in.Partial = s.companyContext(ctx, sess, src.From)
	out := domain.SenderInsight{
		Category: domain.Classify(src.Headers, src.From),
		Shield:   domain.AssessSender(in),
	}
	if domain.UnsubscribeAllowedIn(role) {
		out.Unsubscribe = domain.ParseUnsubscribe(src.Headers)
	}
	if len(src.From) > 0 {
		sender := src.From[0]
		out.Sender = &sender
	}
	return out, nil
}

// insightSource lee las cabeceras del mensaje y el papel de su carpeta en una sola conexion.
func (s *Service) insightSource(ctx context.Context, sess domain.Session, folder string, uid uint32) (domain.InsightSource, domain.FolderRole, error) {
	var src domain.InsightSource
	var role domain.FolderRole
	err := s.withMailbox(ctx, sess, func(mb ports.Mailbox) error {
		folders, err := mb.Folders(ctx, false)
		if err != nil {
			return err
		}
		if f, ok := domain.FindFolder(folders, folder); ok {
			role = f.Role
		}
		src, err = mb.Insight(ctx, folder, uid)
		return err
	})
	return src, role, err
}

// companyContext reune lo que el escudo sabe de la empresa: sus dominios (el del buzon, los de sus
// remitentes y los de los companeros que aparezcan) y los companeros cuyo nombre o direccion casa con
// el nombre visible del remitente. Todo sale de mail-directory, que solo busca en la empresa del
// buzon de la sesion.
func (s *Service) companyContext(ctx context.Context, sess domain.Session, from []domain.Address) ([]string, []domain.AddressBookEntry, bool) {
	partial := false
	own := []string{domain.DomainOf(sess.Username)}
	identities, err := s.directory.SenderIdentities(ctx, sess.Username)
	if err != nil {
		s.logger.Warn("webmail: escudo sin los remitentes del buzon", zap.String("username", sess.Username), zap.Error(err))
		partial = true
	}
	for _, id := range identities {
		own = append(own, domain.DomainOf(id))
	}
	if len(from) == 0 {
		return own, nil, partial
	}
	sender := from[0]
	search := func(query string, limit int) []domain.AddressBookEntry {
		entries, err := s.addressBook.Search(ctx, sess.Username, query, limit)
		if err != nil {
			var verr *domain.ValidationError
			if !errors.As(err, &verr) {
				s.logger.Warn("webmail: escudo sin el directorio de la empresa", zap.String("username", sess.Username), zap.Error(err))
				partial = true
			}
			return nil
		}
		return entries
	}
	senderDomain := domain.DomainOf(sender.Email)
	if senderDomain != "" && !containsFold(own, senderDomain) {
		for _, e := range search("@"+senderDomain, domainSearchLimit) {
			if domain.DomainOf(e.Address) == senderDomain {
				own = append(own, senderDomain)
				break
			}
		}
	}
	var colleagues []domain.AddressBookEntry
	if name := strings.TrimSpace(sender.Name); utf8.RuneCountInString(name) >= 3 {
		colleagues = search(name, colleagueSearchLimit)
		for _, c := range colleagues {
			own = append(own, domain.DomainOf(c.Address))
		}
	}
	return own, colleagues, partial
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// UnsubscribeResult dice como se hizo la baja y contra quien (el host de la URL o la direccion).
type UnsubscribeResult struct {
	Method domain.UnsubscribeMethod
	Target string
}

// Unsubscribe da de baja del boletin del mensaje. La URL o la direccion salen de las cabeceras del
// mensaje guardado, nunca de la peticion. La baja en un clic la hace el servicio contra una URL
// publica; la de correo sale del propio buzon por el mismo camino que un envio normal. Una pagina de
// baja sin POST de un clic no se visita: la abre el usuario.
func (s *Service) Unsubscribe(ctx context.Context, sess domain.Session, folder string, uid uint32) (UnsubscribeResult, error) {
	if err := domain.ValidateFolderName(folder); err != nil {
		return UnsubscribeResult{}, err
	}
	src, role, err := s.insightSource(ctx, sess, folder, uid)
	if err != nil {
		return UnsubscribeResult{}, err
	}
	if !domain.UnsubscribeAllowedIn(role) {
		return UnsubscribeResult{}, domain.ErrUnsubscribeNotAvailable
	}
	u := domain.ParseUnsubscribe(src.Headers)
	switch u.Method {
	case domain.UnsubscribeOneClick:
		host := u.Host()
		if err := s.unsubscriber.OneClick(ctx, u.URL); err != nil {
			s.logger.Warn("webmail: baja en un clic rechazada", zap.String("username", sess.Username), zap.String("host", host), zap.Error(err))
			return UnsubscribeResult{}, err
		}
		s.logger.Info("webmail: baja en un clic hecha", zap.String("username", sess.Username), zap.String("host", host))
		return UnsubscribeResult{Method: u.Method, Target: host}, nil
	case domain.UnsubscribeMailto:
		if err := s.sendUnsubscribeMail(ctx, sess, *u.Mailto); err != nil {
			return UnsubscribeResult{}, err
		}
		return UnsubscribeResult{Method: u.Method, Target: u.Mailto.Address.Email}, nil
	}
	return UnsubscribeResult{}, domain.ErrUnsubscribeNotAvailable
}

// sendUnsubscribeMail envia el correo de baja desde el propio buzon, sin copia en Enviados: es una
// orden al remitente del boletin, no una conversacion.
func (s *Service) sendUnsubscribeMail(ctx context.Context, sess domain.Session, target domain.MailtoTarget) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.SendTimeout)
	defer cancel()
	d := domain.Draft{To: []domain.Address{target.Address}, Subject: target.Subject, Text: target.Body}
	out, wire, _, err := s.build(ctx, sess, d)
	if err != nil {
		return err
	}
	if err := s.sender.Send(ctx, sess.Username, out.From.Email, out.Recipients(), wire); err != nil {
		s.logger.Warn("webmail: correo de baja rechazado", zap.String("username", sess.Username), zap.String("to", target.Address.Email), zap.Error(err))
		return err
	}
	s.logger.Info("webmail: correo de baja enviado", zap.String("username", sess.Username), zap.String("to", target.Address.Email))
	return nil
}
