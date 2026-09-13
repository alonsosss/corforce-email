package postgres

import "testing"

// Las sentencias de los agregados se generan: aqui se fija su forma exacta para que un
// cambio en el generador se vea en la revision.
func TestSentenciasDeAgregado(t *testing.T) {
	wantUpsert := "INSERT INTO analytics.daily_class_stats (tenant_id, day, class, sent, delivered, bounced_hard, bounced_soft, complained, opened_unique, clicked_unique, unsubscribed, failed) " +
		"VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) ON CONFLICT (tenant_id, day, class) DO UPDATE SET " +
		"sent = daily_class_stats.sent + EXCLUDED.sent, delivered = daily_class_stats.delivered + EXCLUDED.delivered, " +
		"bounced_hard = daily_class_stats.bounced_hard + EXCLUDED.bounced_hard, bounced_soft = daily_class_stats.bounced_soft + EXCLUDED.bounced_soft, " +
		"complained = daily_class_stats.complained + EXCLUDED.complained, opened_unique = daily_class_stats.opened_unique + EXCLUDED.opened_unique, " +
		"clicked_unique = daily_class_stats.clicked_unique + EXCLUDED.clicked_unique, unsubscribed = daily_class_stats.unsubscribed + EXCLUDED.unsubscribed, " +
		"failed = daily_class_stats.failed + EXCLUDED.failed"
	if classAggregate.upsert != wantUpsert {
		t.Fatalf("upsert:\n%s", classAggregate.upsert)
	}
	wantIncrement := "UPDATE analytics.daily_domain_stats SET sent = sent + $5, delivered = delivered + $6, bounced_hard = bounced_hard + $7, " +
		"bounced_soft = bounced_soft + $8, complained = complained + $9, opened_unique = opened_unique + $10, clicked_unique = clicked_unique + $11, " +
		"unsubscribed = unsubscribed + $12, failed = failed + $13 WHERE tenant_id = $1 AND day = $2 AND class = $3 AND recipient_domain = $4"
	if domainAggregate.increment != wantIncrement {
		t.Fatalf("increment:\n%s", domainAggregate.increment)
	}
}
