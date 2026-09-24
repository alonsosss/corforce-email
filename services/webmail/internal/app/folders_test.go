package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func withFolders(h *harness) {
	h.mb.folders = []domain.Folder{
		{Name: "INBOX", Delimiter: "/", Role: domain.RoleInbox, Selectable: true},
		{Name: "Sent", Delimiter: "/", Role: domain.RoleSent, Selectable: true},
		{Name: "Drafts", Delimiter: "/", Role: domain.RoleDrafts, Selectable: true},
		{Name: "Trash", Delimiter: "/", Role: domain.RoleTrash, Selectable: true},
		{Name: "Junk", Delimiter: "/", Role: domain.RoleJunk, Selectable: true},
		{Name: "Scheduled", Delimiter: "/", Role: domain.RoleScheduled, Selectable: true},
		{Name: "Clientes", Delimiter: "/", Selectable: true},
		{Name: "Clientes/2026", Delimiter: "/", Selectable: true},
		{Name: "Viejos", Delimiter: "/", Selectable: true},
	}
}

func TestCrearCarpeta(t *testing.T) {
	h := newHarness(t)
	withFolders(h)
	_, sess := h.login(t)
	ctx := context.Background()
	f, err := h.svc.CreateFolder(ctx, sess, "Clientes/2027")
	if err != nil || f.Name != "Clientes/2027" || f.Delimiter != "/" || f.Role != domain.RoleNone {
		t.Fatalf("%+v %v", f, err)
	}
	if strings.Join(h.mb.created, ",") != "Clientes/2027" {
		t.Fatalf("creadas: %v", h.mb.created)
	}
	if _, err := h.svc.CreateFolder(ctx, sess, "Viejos"); !errors.Is(err, domain.ErrFolderExists) {
		t.Fatalf("existente: %v", err)
	}
	var verr *domain.ValidationError
	for _, bad := range []string{"INBOX", "Scheduled", "", "a//b"} {
		if _, err := h.svc.CreateFolder(ctx, sess, bad); !errors.As(err, &verr) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if len(h.mb.created) != 1 {
		t.Fatalf("un nombre invalido no llega a IMAP: %v", h.mb.created)
	}
}

func TestRenombrarCarpeta(t *testing.T) {
	h := newHarness(t)
	withFolders(h)
	_, sess := h.login(t)
	ctx := context.Background()
	f, err := h.svc.RenameFolder(ctx, sess, "Clientes", "Cartera")
	if err != nil || f.Name != "Cartera" || strings.Join(h.mb.renamed, ",") != "Clientes->Cartera" {
		t.Fatalf("%+v %v %v", f, err, h.mb.renamed)
	}
	for _, name := range []string{"INBOX", "Sent", "Trash", "Junk", "Scheduled", "Drafts"} {
		if _, err := h.svc.RenameFolder(ctx, sess, name, "Otra"); !errors.Is(err, domain.ErrFolderProtected) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := h.svc.RenameFolder(ctx, sess, "Viejos", "Clientes"); !errors.Is(err, domain.ErrFolderExists) {
		t.Errorf("destino existente: %v", err)
	}
	if _, err := h.svc.RenameFolder(ctx, sess, "NoEsta", "Otra"); !errors.Is(err, domain.ErrFolderNotFound) {
		t.Errorf("inexistente: %v", err)
	}
	if len(h.mb.renamed) != 1 {
		t.Fatalf("ninguna regla rota llega a IMAP: %v", h.mb.renamed)
	}
}

func TestBorrarCarpeta(t *testing.T) {
	h := newHarness(t)
	withFolders(h)
	_, sess := h.login(t)
	ctx := context.Background()
	if err := h.svc.DeleteFolder(ctx, sess, "Clientes"); !errors.Is(err, domain.ErrFolderHasChildren) {
		t.Fatalf("con subcarpetas: %v", err)
	}
	if err := h.svc.DeleteFolder(ctx, sess, "Scheduled"); !errors.Is(err, domain.ErrFolderProtected) {
		t.Fatalf("protegida: %v", err)
	}
	if err := h.svc.DeleteFolder(ctx, sess, "Viejos"); err != nil || strings.Join(h.mb.deleted, ",") != "Viejos" {
		t.Fatalf("%v %v", err, h.mb.deleted)
	}
}

func TestVaciarSoloPapeleraYSpam(t *testing.T) {
	h := newHarness(t)
	withFolders(h)
	h.mb.emptyN = 4
	_, sess := h.login(t)
	ctx := context.Background()
	for _, name := range []string{"Trash", "Junk"} {
		if n, err := h.svc.EmptyFolder(ctx, sess, name); err != nil || n != 4 {
			t.Errorf("%s: %d %v", name, n, err)
		}
	}
	for _, name := range []string{"INBOX", "Viejos", "Scheduled"} {
		if _, err := h.svc.EmptyFolder(ctx, sess, name); !errors.Is(err, domain.ErrFolderNotEmptiable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := h.svc.EmptyFolder(ctx, sess, "NoEsta"); !errors.Is(err, domain.ErrFolderNotFound) {
		t.Errorf("inexistente: %v", err)
	}
	if strings.Join(h.mb.emptied, ",") != "Trash,Junk" {
		t.Fatalf("vaciadas: %v", h.mb.emptied)
	}
}

func TestLoteCuentaSoloLosQueExisten(t *testing.T) {
	h := newHarness(t)
	h.mb.missing = map[uint32]bool{9: true}
	_, sess := h.login(t)
	ctx := context.Background()

	move, _ := domain.NewBatch("INBOX", []uint32{1, 2, 9}, "move", nil, nil, "Archive")
	res, err := h.svc.Batch(ctx, sess, "INBOX", move)
	if err != nil || res.Affected != 2 || res.Permanent || strings.Join(h.mb.moved, ",") != "INBOX:1->Archive,INBOX:2->Archive" {
		t.Fatalf("mover: %+v %v %v", res, err, h.mb.moved)
	}
	flags, _ := domain.NewBatch("INBOX", []uint32{1, 2}, "flags", []string{`\Seen`}, nil, "")
	if res, err = h.svc.Batch(ctx, sess, "INBOX", flags); err != nil || res.Affected != 2 || len(h.mb.flagged) != 1 {
		t.Fatalf("marcar: %+v %v", res, err)
	}
	del, _ := domain.NewBatch("INBOX", []uint32{3, 4}, "delete", nil, nil, "")
	if res, err = h.svc.Batch(ctx, sess, "INBOX", del); err != nil || res.Affected != 2 || res.Permanent {
		t.Fatalf("a la papelera: %+v %v", res, err)
	}
	fromTrash, _ := domain.NewBatch("Trash", []uint32{5, 9}, "delete", nil, nil, "")
	if res, err = h.svc.Batch(ctx, sess, "Trash", fromTrash); err != nil || res.Affected != 1 || !res.Permanent {
		t.Fatalf("desde la papelera: %+v %v", res, err)
	}
	if strings.Join(h.mb.expunged, ",") != "Trash:5" {
		t.Fatalf("borrado definitivo: %v", h.mb.expunged)
	}
	none, _ := domain.NewBatch("INBOX", []uint32{9}, "move", nil, nil, "Archive")
	if res, err = h.svc.Batch(ctx, sess, "INBOX", none); err != nil || res.Affected != 0 {
		t.Fatalf("ninguno existe: %+v %v", res, err)
	}
	// Un mensaje suelto que ya no esta sigue siendo 404.
	if err := h.svc.Move(ctx, sess, "INBOX", 9, "Archive"); !errors.Is(err, domain.ErrMessageNotFound) {
		t.Fatalf("mover uno inexistente: %v", err)
	}
	if _, err := h.svc.Delete(ctx, sess, "INBOX", 9); !errors.Is(err, domain.ErrMessageNotFound) {
		t.Fatalf("borrar uno inexistente: %v", err)
	}
}

func TestLaBusquedaAvanzadaLlegaAlBuzon(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	q, _ := domain.NewListQuery(1, 10, "")
	q, _ = q.WithFilter(domain.SearchFilter{From: "ana", Flagged: true, HasAttachments: true})
	if _, err := h.svc.ListMessages(context.Background(), sess, "INBOX", q); err != nil {
		t.Fatal(err)
	}
	if f := h.mb.listQuery.Filter; f.From != "ana" || !f.Flagged || !f.HasAttachments {
		t.Fatalf("%+v", f)
	}
}

func TestOriginalAcotadoPorElTopeDeDescarga(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	ctx := context.Background()
	stored, _ := h.mb.Append(ctx, "INBOX", []byte("id=m1@x;cuerpo"), nil, h.clock.Now())
	var got string
	err := h.svc.StreamRaw(ctx, sess, "INBOX", stored.UID, func(_ domain.StoredMessage, body io.Reader) error {
		raw, err := io.ReadAll(body)
		got = string(raw)
		return err
	})
	if err != nil || got != "id=m1@x;cuerpo" {
		t.Fatalf("%q %v", got, err)
	}
	h.svc.cfg.MaxAttachmentBytes = 4
	if err := h.svc.StreamRaw(ctx, sess, "INBOX", stored.UID, nil); !errors.Is(err, domain.ErrMessageTooLarge) {
		t.Fatalf("tope: %v", err)
	}
}
