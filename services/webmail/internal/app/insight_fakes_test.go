package app

import (
	"context"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

// fakeInsight es lo que el buzon falso devuelve para conversaciones y fichas, y con que se le pidio.
type fakeInsight struct {
	threads         domain.ThreadPage
	threadsQuery    domain.ListQuery
	conversation    []domain.ConversationMessage
	conversationErr error
	related         []domain.ConversationMessage
	relatedErr      error
	relatedFolder   string
	relatedIDs      []string
	source          domain.InsightSource
	sourceErr       error
	sourceFor       string
}

func (m *fakeMailbox) ListThreads(_ context.Context, folder string, q domain.ListQuery) (domain.ThreadPage, error) {
	m.listed, m.insight.threadsQuery = folder, q
	return m.insight.threads, nil
}

func (m *fakeMailbox) Conversation(_ context.Context, folder string, _ uint32, max int) ([]domain.ConversationMessage, error) {
	m.listed = folder
	msgs := m.insight.conversation
	if len(msgs) > max {
		msgs = msgs[:max]
	}
	return msgs, m.insight.conversationErr
}

func (m *fakeMailbox) Related(_ context.Context, folder string, ids []string, _ int) ([]domain.ConversationMessage, error) {
	m.insight.relatedFolder, m.insight.relatedIDs = folder, ids
	return m.insight.related, m.insight.relatedErr
}

func (m *fakeMailbox) Insight(_ context.Context, folder string, _ uint32) (domain.InsightSource, error) {
	m.insight.sourceFor = folder
	return m.insight.source, m.insight.sourceErr
}

// fakeUnsubscriber anota la URL de la baja en un clic.
type fakeUnsubscriber struct {
	targets []string
	err     error
}

func (u *fakeUnsubscriber) OneClick(_ context.Context, target string) error {
	u.targets = append(u.targets, target)
	return u.err
}

// queryBook es una libreta que responde por consulta exacta.
type queryBook struct {
	byQuery map[string][]domain.AddressBookEntry
	err     error
	queries []string
}

func (b *queryBook) Search(_ context.Context, _, query string, _ int) ([]domain.AddressBookEntry, error) {
	b.queries = append(b.queries, query)
	return b.byQuery[query], b.err
}
