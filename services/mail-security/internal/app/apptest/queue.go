package apptest

import (
	"context"
	"sync"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

// Queue implementa ports.EngineQueue en memoria: anota lo que se le pide y devuelve lo que la prueba prepare.
type Queue struct {
	mu      sync.Mutex
	Listing domain.QueueListing
	// Err lo devuelven todas las operaciones.
	Err error

	ListLimit int
	Applied   []string
	Flushed   int
}

func NewQueue() *Queue {
	return &Queue{Listing: domain.QueueListing{Items: []domain.QueueMessage{}}}
}

func (q *Queue) List(_ context.Context, limit int) (domain.QueueListing, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ListLimit = limit
	return q.Listing, q.Err
}

func (q *Queue) Apply(_ context.Context, action domain.QueueAction, id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.Applied = append(q.Applied, string(action)+" "+id)
	return q.Err
}

func (q *Queue) Flush(context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.Flushed++
	return q.Err
}
