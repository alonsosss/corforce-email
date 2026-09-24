//go:build integration

package postgres

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/google/uuid"
)

func TestRecordatoriosContraPostgres(t *testing.T) {
	f := newWebmailFixture(t)
	snooze := func(messageID string, due time.Time) *domain.Reminder {
		t.Helper()
		r, err := f.uc.CreateReminder(f.internal, app.CreateReminderRequest{
			Username: f.ana.Username, Kind: domain.ReminderSnooze, MessageID: messageID, Folder: "Snoozed",
			UIDValidity: 4294967295, UID: 17, ReturnFolder: "Clientes/Norte", DueAt: due, Subject: "Factura",
			Addresses: []string{"proveedor@otro.example"},
		})
		if err != nil {
			t.Fatalf("posponer %s: %v", messageID, err)
		}
		return r
	}
	future := snooze("futuro-"+f.suffix+"@x", time.Now().Add(time.Hour))
	if future.UIDValidity != 4294967295 || future.ReturnFolder != "Clientes/Norte" || future.CreatedAt.IsZero() || len(future.Addresses) != 1 {
		t.Fatalf("fila creada: %+v", future)
	}
	if _, err := f.uc.CreateReminder(f.internal, app.CreateReminderRequest{
		Username: f.ana.Username, Kind: domain.ReminderSnooze, MessageID: future.MessageID, Folder: "Snoozed",
		UIDValidity: 1, UID: 2, ReturnFolder: "INBOX", DueAt: time.Now().Add(time.Hour),
	}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("el mismo mensaje no se pospone dos veces: %v", err)
	}
	// Un seguimiento del mismo mensaje es otro tipo y si cabe; uno sin UID (envio programado) guarda NULL.
	follow, err := f.uc.CreateReminder(f.internal, app.CreateReminderRequest{
		Username: f.ana.Username, Kind: domain.ReminderFollowUp, MessageID: future.MessageID, Folder: "Sent",
		DueAt: time.Now().Add(72 * time.Hour), Subject: "Presupuesto", Addresses: []string{"cliente@otro.example"},
	})
	if err != nil || follow.UID != 0 || follow.UIDValidity != 0 {
		t.Fatalf("seguimiento sin UID: %+v %v", follow, err)
	}
	// Dos pospuestos sin Message-ID no chocan: se localizan por su UID.
	for i := 0; i < 2; i++ {
		if _, err := f.uc.CreateReminder(f.internal, app.CreateReminderRequest{
			Username: f.ana.Username, Kind: domain.ReminderSnooze, Folder: "Snoozed", UIDValidity: 1, UID: uint32(40 + i),
			ReturnFolder: "INBOX", DueAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("pospuesto sin Message-ID %d: %v", i, err)
		}
	}
	if n, err := f.asRole(t, "mail_app", f.tenantB, `SELECT count(*) FROM mail.mailbox_reminders WHERE id = $1`, future.ID); err != nil || n != 0 {
		t.Fatalf("mail_app de B ve el recordatorio de A: %d %v", n, err)
	}
	if n, err := f.asRole(t, "mail_app", f.tenantA, `SELECT count(*) FROM mail.mailbox_reminders WHERE id = $1`, future.ID); err != nil || n != 1 {
		t.Fatalf("mail_app de A no ve su recordatorio: %d %v", n, err)
	}
	_, err = f.asRole(t, "mail_engine", uuid.Nil, `SELECT count(*) FROM mail.mailbox_reminders`)
	expectSQLState(t, err, "42501", "mail_engine lee los recordatorios")
	if list, err := f.uc.ListReminders(f.internal, f.luis.Username, domain.ReminderSnooze); err != nil || len(list) != 0 {
		t.Fatalf("otro buzon lista recordatorios ajenos: %+v %v", list, err)
	}
	if _, err := f.uc.RescheduleReminder(f.internal, f.luis.Username, future.ID, time.Now().Add(2*time.Hour)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("reprogramar uno ajeno: %v", err)
	}
	moved, err := f.uc.RescheduleReminder(f.internal, f.ana.Username, future.ID, time.Now().Add(2*time.Hour))
	if err != nil || !moved.DueAt.After(time.Now().Add(90*time.Minute)) {
		t.Fatalf("reprogramar: %+v %v", moved, err)
	}

	// ── Reclamacion concurrente: cada recordatorio vencido lo toma un solo trabajador ──
	const due = 24
	mine := map[uuid.UUID]bool{}
	for i := 0; i < due; i++ {
		r := snooze("vencido-"+f.suffix+"-"+uuid.NewString()+"@x", time.Now().Add(-30*time.Second))
		mine[r.ID] = true
	}
	var (
		mu      sync.Mutex
		claimed = map[uuid.UUID]int{}
		wg      sync.WaitGroup
		start   = make(chan struct{})
		errs    = make(chan error, 6)
	)
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				rows, err := f.uc.ClaimReminders(f.internal, 3, 60)
				if err != nil {
					errs <- err
					return
				}
				if len(rows) == 0 {
					return
				}
				mu.Lock()
				for _, r := range rows {
					claimed[r.ID]++
					if r.Status != domain.ReminderRunning || r.Attempts != 1 || r.LeaseUntil == nil {
						errs <- errors.New("fila reclamada sin estado, intento o arriendo: " + r.ID.String())
					}
				}
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for id := range mine {
		if claimed[id] != 1 {
			t.Fatalf("la fila %s se reclamo %d veces", id, claimed[id])
		}
	}
	if claimed[future.ID] != 0 || claimed[follow.ID] != 0 {
		t.Fatal("una fila futura no se reclama")
	}

	// ── Cierre: reintento con espera, hecho con su resultado, y uno no reclamado no se cierra ──
	var retried, returned uuid.UUID
	for id := range mine {
		if retried == uuid.Nil {
			retried = id
		} else {
			returned = id
			break
		}
	}
	r, err := f.uc.FinishReminder(f.internal, retried, domain.ReminderOutcome{Status: domain.ReminderFailed, Error: "imap caido", Retry: true})
	if err != nil || r.Status != domain.ReminderPending || r.LeaseUntil != nil || !r.DueAt.After(time.Now().Add(30*time.Second)) || r.LastError != "imap caido" {
		t.Fatalf("reintento: %+v %v", r, err)
	}
	done, err := f.uc.FinishReminder(f.internal, returned, domain.ReminderOutcome{Status: domain.ReminderDone, Result: domain.ReminderReturned})
	if err != nil || done.Status != domain.ReminderDone || done.Result != domain.ReminderReturned || done.DoneAt == nil {
		t.Fatalf("hecho: %+v %v", done, err)
	}
	if _, err := f.uc.FinishReminder(f.internal, returned, domain.ReminderOutcome{Status: domain.ReminderDone, Result: domain.ReminderReturned}); !errors.Is(err, domain.ErrReminderNotClaimed) {
		t.Fatalf("cerrar dos veces: %v", err)
	}
	if _, err := f.uc.FinishReminder(f.internal, uuid.New(), domain.ReminderOutcome{Status: domain.ReminderCanceled}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("fila inexistente: %v", err)
	}
	if err := f.uc.CancelReminder(f.internal, f.ana.Username, returned); !errors.Is(err, domain.ErrReminderNotPending) {
		t.Fatalf("uno hecho no se cancela: %v", err)
	}
	if err := f.uc.CancelReminder(f.internal, f.ana.Username, retried); err != nil {
		t.Fatalf("cancelar el pendiente: %v", err)
	}

	// ── Arriendos vencidos y purga de los terminados viejos ──
	var expiredRetry, expiredDone uuid.UUID
	for id := range mine {
		if id == retried || id == returned {
			continue
		}
		if expiredRetry == uuid.Nil {
			expiredRetry = id
		} else {
			expiredDone = id
			break
		}
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE mail.mailbox_reminders SET lease_until = now() - interval '1 second', attempts = 2 WHERE id = $1`, expiredRetry); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE mail.mailbox_reminders SET lease_until = now() - interval '1 second', attempts = $2 WHERE id = $1`, expiredDone, domain.MaxReminderAttempts); err != nil {
		t.Fatal(err)
	}
	var old uuid.UUID
	if err := f.pool.QueryRow(f.ctx,
		`INSERT INTO mail.mailbox_reminders (tenant_id, username, kind, message_id, folder, due_at, status, result, done_at, updated_at)
		 VALUES ($1, $2, 'follow_up', 'viejo@x', 'Sent', now() - interval '40 days', 'done', 'replied', now() - interval '40 days', now() - interval '31 days')
		 RETURNING id`, f.tenantA, f.ana.Username).Scan(&old); err != nil {
		t.Fatal(err)
	}
	rows, err := f.uc.ClaimReminders(f.internal, domain.MaxScheduledClaim, 60)
	if err != nil {
		t.Fatal(err)
	}
	var again bool
	for _, row := range rows {
		if row.ID == expiredRetry {
			again = row.Attempts == 3
		}
		if row.ID == expiredDone {
			t.Fatal("una fila sin intentos no se vuelve a reclamar")
		}
	}
	if !again {
		t.Fatalf("el arriendo vencido con intentos se reclama con un intento mas: %+v", rows)
	}
	var status, lastError string
	if err := f.pool.QueryRow(f.ctx, `SELECT status, last_error FROM mail.mailbox_reminders WHERE id = $1`, expiredDone).Scan(&status, &lastError); err != nil ||
		status != domain.ReminderFailed || lastError != domain.ReminderLeaseExpiredError {
		t.Fatalf("arriendo vencido sin intentos: %s %q %v", status, lastError, err)
	}
	var purged int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM mail.mailbox_reminders WHERE id = $1`, old).Scan(&purged); err != nil || purged != 0 {
		t.Fatalf("la fila vieja no se purgo: %d %v", purged, err)
	}

	// ── Restricciones de la tabla ──
	_, err = f.pool.Exec(f.ctx,
		`INSERT INTO mail.mailbox_reminders (tenant_id, username, kind, message_id, folder, due_at, status)
		 VALUES ($1, $2, 'snooze', 'x@y', 'Snoozed', now(), 'pending')`, f.tenantA, f.ana.Username)
	expectSQLState(t, err, "23514", "pospuesto sin UID ni carpeta de vuelta")
	_, err = f.pool.Exec(f.ctx,
		`INSERT INTO mail.mailbox_reminders (tenant_id, username, kind, folder, uid_validity, due_at)
		 VALUES ($1, $2, 'follow_up', 'Sent', 1, now())`, f.tenantA, f.ana.Username)
	expectSQLState(t, err, "23514", "uid_validity sin uid")
	_, err = f.pool.Exec(f.ctx,
		`INSERT INTO mail.mailbox_reminders (tenant_id, username, kind, message_id, folder, due_at, status)
		 VALUES ($1, $2, 'follow_up', 'z@y', 'Sent', now(), 'done')`, f.tenantA, f.ana.Username)
	expectSQLState(t, err, "23514", "hecho sin resultado")

	// ── Borrar el buzon borra sus recordatorios ──
	if err := f.uc.DeleteMailbox(f.ctxA, f.tenantA, f.ana.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM mail.mailbox_reminders WHERE username = $1`, f.ana.Username).Scan(&left); err != nil || left != 0 {
		t.Fatalf("quedaron recordatorios del buzon borrado: %d %v", left, err)
	}
}

func TestRespuestasRapidasContraPostgres(t *testing.T) {
	f := newWebmailFixture(t)
	q, err := f.uc.CreateQuickReply(f.internal, f.ana.Username, app.QuickReplyInput{Name: "Gracias", HTML: "<p>Hola {nombre}</p>", Text: "Hola {nombre}"})
	if err != nil || q.CreatedAt.IsZero() || q.TenantID != f.tenantA {
		t.Fatalf("crear: %+v %v", q, err)
	}
	if _, err := f.uc.CreateQuickReply(f.internal, f.ana.Username, app.QuickReplyInput{Name: "GRACIAS", Text: "x"}); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("nombre repetido sin distinguir mayusculas: %v", err)
	}
	if _, err := f.uc.CreateQuickReply(f.internal, f.luis.Username, app.QuickReplyInput{Name: "Gracias", Text: "x"}); err != nil {
		t.Fatalf("otro buzon puede usar el mismo nombre: %v", err)
	}
	if n, err := f.asRole(t, "mail_app", f.tenantB, `SELECT count(*) FROM mail.mailbox_quick_replies WHERE id = $1`, q.ID); err != nil || n != 0 {
		t.Fatalf("mail_app de B ve la respuesta de A: %d %v", n, err)
	}
	_, err = f.asRole(t, "mail_engine", uuid.Nil, `SELECT count(*) FROM mail.mailbox_quick_replies`)
	expectSQLState(t, err, "42501", "mail_engine lee las respuestas rapidas")
	if _, err := f.uc.UpdateQuickReply(f.internal, f.luis.Username, q.ID, app.QuickReplyInput{Name: "Mia", Text: "x"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cambiar una ajena: %v", err)
	}
	updated, err := f.uc.UpdateQuickReply(f.internal, f.ana.Username, q.ID, app.QuickReplyInput{Name: "Agradecer", Text: "Gracias, {nombre}"})
	if err != nil || updated.Name != "Agradecer" || updated.HTML != "" || !updated.UpdatedAt.After(q.CreatedAt.Add(-time.Second)) {
		t.Fatalf("cambiar: %+v %v", updated, err)
	}
	if _, err := f.uc.CreateQuickReply(f.internal, f.ana.Username, app.QuickReplyInput{Name: "Bienvenida", Text: "Hola"}); err != nil {
		t.Fatal(err)
	}
	list, err := f.uc.QuickReplies(f.internal, f.ana.Username)
	if err != nil || len(list) != 2 || list[0].Name != "Agradecer" || list[1].Name != "Bienvenida" {
		t.Fatalf("listado por nombre: %+v %v", list, err)
	}
	if err := f.uc.DeleteQuickReply(f.internal, f.luis.Username, q.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("borrar una ajena: %v", err)
	}
	if err := f.uc.DeleteQuickReply(f.internal, f.ana.Username, q.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(f.ctx, `INSERT INTO mail.mailbox_quick_replies (tenant_id, username, name) VALUES ($1, $2, 'vacia')`, f.tenantA, f.ana.Username)
	expectSQLState(t, err, "23514", "respuesta sin contenido")

	if err := f.uc.DeleteMailbox(f.ctxA, f.tenantA, f.ana.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM mail.mailbox_quick_replies WHERE username = $1`, f.ana.Username).Scan(&left); err != nil || left != 0 {
		t.Fatalf("quedaron respuestas del buzon borrado: %d %v", left, err)
	}
}
