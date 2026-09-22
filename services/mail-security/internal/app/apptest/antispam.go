package apptest

import (
	"context"
	"sync"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

// Antispam implementa ports.AntispamInspector en memoria: devuelve lo que la prueba prepare y cuenta
// las lecturas.
type Antispam struct {
	mu           sync.Mutex
	Stat         domain.RspamdStats
	Rows         []domain.RspamdHistoryRow
	Err          error
	StatReads    int
	HistoryReads int
}

func NewAntispam() *Antispam {
	return &Antispam{Stat: domain.RspamdStats{Actions: map[string]int64{}, FuzzyHashes: map[string]int64{}, Statfiles: []domain.RspamdStatfile{}}}
}

func (a *Antispam) Stats(context.Context) (domain.RspamdStats, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.StatReads++
	return a.Stat, a.Err
}

func (a *Antispam) History(context.Context) ([]domain.RspamdHistoryRow, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.HistoryReads++
	return append([]domain.RspamdHistoryRow(nil), a.Rows...), a.Err
}
