package imap

import (
	"bufio"
	"bytes"
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	imaplib "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/textproto"
)

const (
	// threadFetchBatch es cuantos mensajes se leen por orden al agrupar sin THREAD.
	threadFetchBatch = 500
	// participantScan es cuantos mensajes de cada conversacion se leen para resumir sus remitentes.
	participantScan = 10
)

// categorySection pide las cabeceras que deciden la pestana de la bandeja inteligente.
func categorySection() *imaplib.FetchItemBodySection {
	return &imaplib.FetchItemBodySection{Specifier: imaplib.PartSpecifierHeader, HeaderFields: domain.CategoryHeaderKeys, Peek: true}
}

func insightSection() *imaplib.FetchItemBodySection {
	return &imaplib.FetchItemBodySection{Specifier: imaplib.PartSpecifierHeader, HeaderFields: domain.InsightHeaderKeys, Peek: true}
}

// parseHeaderFields lee un HEADER.FIELDS y despliega cada valor en una sola linea.
func parseHeaderFields(raw []byte) domain.MessageHeaders {
	out := domain.MessageHeaders{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return out
	}
	h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return out
	}
	fields := h.Fields()
	for fields.Next() {
		out[fields.Key()] = append(out[fields.Key()], strings.Join(strings.Fields(fields.Value()), " "))
	}
	return out
}

// Las reglas de domain.Classify en IMAP SEARCH: HEADER con valor vacio casa con la cabecera
// presente, con valor, con la subcadena sin distinguir mayusculas (RFC 3501 6.4.4).
func headerHas(key, value string) imaplib.SearchCriteria {
	return imaplib.SearchCriteria{Header: []imaplib.SearchCriteriaHeaderField{{Key: key, Value: value}}}
}

func anyOf(list ...imaplib.SearchCriteria) imaplib.SearchCriteria {
	if len(list) == 1 {
		return list[0]
	}
	return imaplib.SearchCriteria{Or: [][2]imaplib.SearchCriteria{{list[0], anyOf(list[1:]...)}}}
}

func categoryCriteria(c domain.Category) *imaplib.SearchCriteria {
	auto := headerHas(domain.HeaderAutoSubmitted, domain.AutoSubmittedMarker)
	news := anyOf(headerHas(domain.HeaderListUnsubscribe, ""), headerHas(domain.HeaderListID, ""),
		headerHas(domain.HeaderPrecedence, domain.NewsletterPrecedence))
	var weakList []imaplib.SearchCriteria
	for _, p := range domain.NotificationPrecedences {
		weakList = append(weakList, headerHas(domain.HeaderPrecedence, p))
	}
	for _, m := range domain.NotificationSenderMarkers {
		weakList = append(weakList, headerHas("From", m))
	}
	weak := anyOf(weakList...)
	switch c {
	case domain.CategoryNotifications:
		weakNotNews := weak
		weakNotNews.Not = append(weakNotNews.Not, news)
		out := anyOf(auto, weakNotNews)
		return &out
	case domain.CategoryNewsletters:
		out := news
		out.Not = append(out.Not, auto)
		return &out
	case domain.CategoryPrimary:
		return &imaplib.SearchCriteria{Not: []imaplib.SearchCriteria{auto, news, weak}}
	}
	return nil
}

func (m *mailbox) supportsThreadReferences() bool {
	return slices.Contains(m.c.Caps().ThreadAlgorithms(), imaplib.ThreadReferences)
}

func (m *mailbox) ListThreads(ctx context.Context, folder string, q domain.ListQuery) (domain.ThreadPage, error) {
	defer m.watch(ctx)()
	if err := m.selectFolder(folder, false); err != nil {
		return domain.ThreadPage{}, err
	}
	criteria := searchCriteria(q)
	uids, err := m.sortedUIDs(criteria)
	if err != nil {
		return domain.ThreadPage{}, err
	}
	capped := false
	if q.Filter.HasAttachments {
		if uids, capped, err = m.withAttachments(uids); err != nil {
			return domain.ThreadPage{}, err
		}
	}
	groups, scanCapped, err := m.threadGroups(criteria, uids)
	if err != nil {
		return domain.ThreadPage{}, err
	}
	total := len(groups)
	start, end := q.Window(total)
	page := domain.ThreadPage{Total: total, Capped: capped || scanCapped}
	if start == end {
		return page, nil
	}
	page.Items, err = m.summarize(groups[start:end])
	return page, err
}

// threadGroups agrupa uids (del mas reciente al mas antiguo) en conversaciones. Cada grupo y la
// lista de grupos quedan en ese mismo orden. Con THREAD=REFERENCES agrupa el servidor sobre toda
// la carpeta; sin el se agrupan por References e In-Reply-To los ThreadScanLimit mas recientes.
func (m *mailbox) threadGroups(criteria *imaplib.SearchCriteria, uids []imaplib.UID) ([][]imaplib.UID, bool, error) {
	rank := make(map[imaplib.UID]int, len(uids))
	for i, u := range uids {
		rank[u] = i
	}
	if m.supportsThreadReferences() {
		data, err := m.c.UIDThread(&imapclient.ThreadOptions{Algorithm: imaplib.ThreadReferences, SearchCriteria: criteria}).Wait()
		if err != nil {
			return nil, false, mapError(err, nil)
		}
		var groups [][]imaplib.UID
		for i := range data {
			var members []imaplib.UID
			for _, n := range flattenThread(&data[i]) {
				if _, ok := rank[imaplib.UID(n)]; ok {
					members = append(members, imaplib.UID(n))
				}
			}
			if len(members) == 0 {
				continue
			}
			sort.Slice(members, func(a, b int) bool { return rank[members[a]] < rank[members[b]] })
			groups = append(groups, members)
		}
		sort.SliceStable(groups, func(a, b int) bool { return rank[groups[a][0]] < rank[groups[b][0]] })
		return groups, false, nil
	}
	capped := len(uids) > domain.ThreadScanLimit
	if capped {
		uids = uids[:domain.ThreadScanLimit]
	}
	msgs, err := m.threadMessages(uids)
	if err != nil {
		return nil, false, err
	}
	var groups [][]imaplib.UID
	for _, g := range domain.GroupThreads(msgs) {
		members := make([]imaplib.UID, len(g))
		for i, u := range g {
			members[i] = imaplib.UID(u)
		}
		groups = append(groups, members)
	}
	return groups, capped, nil
}

func flattenThread(t *imapclient.ThreadData) []uint32 {
	out := append([]uint32(nil), t.Chain...)
	for i := range t.SubThreads {
		out = append(out, flattenThread(&t.SubThreads[i])...)
	}
	return out
}

// threadMessages lee por lotes los identificadores de cada mensaje, en el orden de uids.
func (m *mailbox) threadMessages(uids []imaplib.UID) ([]domain.ThreadMessage, error) {
	byUID := make(map[imaplib.UID]domain.ThreadMessage, len(uids))
	for start := 0; start < len(uids); start += threadFetchBatch {
		chunk := uids[start:min(start+threadFetchBatch, len(uids))]
		bufs, err := m.c.Fetch(imaplib.UIDSetNum(chunk...), &imaplib.FetchOptions{
			UID: true, Envelope: true, BodySection: []*imaplib.FetchItemBodySection{referencesSection()},
		}).Collect()
		if err != nil {
			return nil, mapError(err, nil)
		}
		for _, b := range bufs {
			tm := domain.ThreadMessage{UID: uint32(b.UID), References: parseReferences(headerSection(b))}
			if env := b.Envelope; env != nil {
				tm.MessageID = bareMessageID(env.MessageID)
				for _, id := range env.InReplyTo {
					tm.InReplyTo = append(tm.InReplyTo, bareMessageID(id))
				}
			}
			byUID[b.UID] = tm
		}
	}
	out := make([]domain.ThreadMessage, 0, len(uids))
	for _, u := range uids {
		if tm, ok := byUID[u]; ok {
			out = append(out, tm)
		}
	}
	return out, nil
}

// summarize resume cada conversacion de la pagina: el ultimo mensaje entero (con su pestana), los
// remitentes de los mas recientes y los no leidos de todos, con tres ordenes para toda la pagina.
func (m *mailbox) summarize(groups [][]imaplib.UID) ([]domain.ThreadSummary, error) {
	var latest, scan, all []imaplib.UID
	for _, g := range groups {
		capped := g[:min(len(g), domain.MaxThreadMessages)]
		latest = append(latest, g[0])
		scan = append(scan, capped[:min(len(capped), participantScan)]...)
		all = append(all, capped...)
	}
	full, err := m.c.Fetch(imaplib.UIDSetNum(latest...), &imaplib.FetchOptions{
		UID: true, Envelope: true, Flags: true, RFC822Size: true, InternalDate: true,
		BodyStructure: &imaplib.FetchItemBodyStructure{Extended: true},
		BodySection:   []*imaplib.FetchItemBodySection{categorySection()},
	}).Collect()
	if err != nil {
		return nil, mapError(err, nil)
	}
	envByUID := map[imaplib.UID]domain.Envelope{}
	for _, b := range full {
		env := envelopeOf(b)
		env.Category = domain.Classify(parseHeaderFields(headerSection(b)), env.From)
		envByUID[b.UID] = env
	}
	partial, err := m.c.Fetch(imaplib.UIDSetNum(scan...), &imaplib.FetchOptions{UID: true, Envelope: true}).Collect()
	if err != nil {
		return nil, mapError(err, nil)
	}
	fromByUID := map[imaplib.UID]domain.Envelope{}
	for _, b := range partial {
		fromByUID[b.UID] = envelopeOf(b)
	}
	unseen, err := m.c.UIDSearch(&imaplib.SearchCriteria{
		UID: []imaplib.UIDSet{imaplib.UIDSetNum(all...)}, NotFlag: []imaplib.Flag{imaplib.FlagSeen},
	}, nil).Wait()
	if err != nil {
		return nil, mapError(err, nil)
	}
	unread := map[imaplib.UID]bool{}
	for _, u := range unseen.AllUIDs() {
		unread[u] = true
	}

	out := make([]domain.ThreadSummary, 0, len(groups))
	for _, g := range groups {
		head, ok := envByUID[g[0]]
		if !ok {
			// El ultimo mensaje desaparecio entre la busqueda y la lectura (otro cliente lo movio).
			continue
		}
		capped := g[:min(len(g), domain.MaxThreadMessages)]
		s := domain.ThreadSummary{Latest: head, Size: len(g), UIDs: make([]uint32, len(capped))}
		var senders []domain.Envelope
		for i, u := range capped {
			s.UIDs[i] = uint32(u)
			if unread[u] {
				s.Unread++
			}
			if env, ok := fromByUID[u]; ok {
				senders = append(senders, env)
			}
		}
		s.Participants = domain.Participants(senders)
		out = append(out, s)
	}
	return out, nil
}

func (m *mailbox) Conversation(ctx context.Context, folder string, uid uint32, max int) ([]domain.ConversationMessage, error) {
	defer m.watch(ctx)()
	if err := m.selectFolder(folder, false); err != nil {
		return nil, err
	}
	target := imaplib.UID(uid)
	all := &imaplib.SearchCriteria{}
	uids, err := m.sortedUIDs(all)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(uids, target) {
		return nil, domain.ErrMessageNotFound
	}
	groups, _, err := m.threadGroups(all, uids)
	if err != nil {
		return nil, err
	}
	members := []imaplib.UID{target}
	for _, g := range groups {
		if slices.Contains(g, target) {
			members = g
			break
		}
	}
	return m.conversationMessages(folder, members[:min(len(members), max)])
}

func (m *mailbox) Related(ctx context.Context, folder string, messageIDs []string, max int) ([]domain.ConversationMessage, error) {
	defer m.watch(ctx)()
	var alternatives []imaplib.SearchCriteria
	for _, id := range messageIDs {
		if !domain.IsValidMessageID(id) {
			continue
		}
		quoted := "<" + id + ">"
		alternatives = append(alternatives, headerHas("Message-ID", quoted), headerHas("In-Reply-To", quoted), headerHas("References", quoted))
	}
	if len(alternatives) == 0 {
		return nil, nil
	}
	if err := m.selectFolder(folder, false); err != nil {
		return nil, err
	}
	criteria := anyOf(alternatives...)
	uids, err := m.sortedUIDs(&criteria)
	if err != nil {
		return nil, err
	}
	return m.conversationMessages(folder, uids[:min(len(uids), max)])
}

// conversationMessages lee los sobres de uids y los devuelve en ese orden.
func (m *mailbox) conversationMessages(folder string, uids []imaplib.UID) ([]domain.ConversationMessage, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	bufs, err := m.c.Fetch(imaplib.UIDSetNum(uids...), &imaplib.FetchOptions{
		UID: true, Envelope: true, Flags: true, RFC822Size: true, InternalDate: true,
		BodyStructure: &imaplib.FetchItemBodyStructure{Extended: true},
		BodySection:   []*imaplib.FetchItemBodySection{categorySection()},
	}).Collect()
	if err != nil {
		return nil, mapError(err, nil)
	}
	byUID := make(map[imaplib.UID]domain.ConversationMessage, len(bufs))
	for _, b := range bufs {
		env := envelopeOf(b)
		env.Category = domain.Classify(parseHeaderFields(headerSection(b)), env.From)
		msg := domain.ConversationMessage{Folder: folder, Envelope: env}
		if b.Envelope != nil {
			msg.MessageID = bareMessageID(b.Envelope.MessageID)
			for _, id := range b.Envelope.InReplyTo {
				if id = bareMessageID(id); id != "" {
					msg.InReplyTo = append(msg.InReplyTo, id)
				}
			}
		}
		byUID[b.UID] = msg
	}
	out := make([]domain.ConversationMessage, 0, len(uids))
	for _, u := range uids {
		if msg, ok := byUID[u]; ok {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (m *mailbox) Insight(ctx context.Context, folder string, uid uint32) (domain.InsightSource, error) {
	defer m.watch(ctx)()
	if err := m.selectFolder(folder, false); err != nil {
		return domain.InsightSource{}, err
	}
	bufs, err := m.c.Fetch(imaplib.UIDSetNum(imaplib.UID(uid)), &imaplib.FetchOptions{
		UID: true, Envelope: true, BodySection: []*imaplib.FetchItemBodySection{insightSection()},
	}).Collect()
	if err != nil {
		return domain.InsightSource{}, mapError(err, nil)
	}
	if len(bufs) == 0 {
		return domain.InsightSource{}, domain.ErrMessageNotFound
	}
	src := domain.InsightSource{Headers: parseHeaderFields(headerSection(bufs[0]))}
	if env := bufs[0].Envelope; env != nil {
		src.From = addressesOf(env.From)
		src.ReplyTo = addressesOf(env.ReplyTo)
	}
	return src, nil
}
