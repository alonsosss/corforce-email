package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func sampleFolders() []Folder {
	return []Folder{
		{Name: "INBOX", Delimiter: "/", Role: RoleInbox, Selectable: true},
		{Name: "Sent", Delimiter: "/", Role: RoleSent, Selectable: true},
		{Name: "Scheduled", Delimiter: "/", Role: RoleScheduled, Selectable: true},
		{Name: "Clientes", Delimiter: "/", Selectable: true},
		{Name: "Clientes/2026", Delimiter: "/", Selectable: true},
		{Name: "Proyectos", Delimiter: "/", Selectable: true},
		{Name: "Proyectos/Trash", Delimiter: "/", Role: RoleTrash, Selectable: true},
	}
}

func TestLaCarpetaDeEnviosProgramadosSeReconocePorNombre(t *testing.T) {
	if RoleByName("scheduled") != RoleScheduled || !IsScheduledFolderName("SCHEDULED") || IsScheduledFolderName("Programados") {
		t.Fatal("papel scheduled por nombre")
	}
	found := false
	for _, r := range SpecialRoles {
		found = found || r == RoleScheduled
	}
	if !found {
		t.Fatal("scheduled debe estar en SpecialRoles (y por tanto en /meta)")
	}
}

func TestLasCarpetasConPapelEINBOXEstanProtegidas(t *testing.T) {
	folders := sampleFolders()
	for _, name := range []string{"INBOX", "Sent", "Scheduled"} {
		if _, err := CheckFolderChangeable(folders, name); !errors.Is(err, ErrFolderProtected) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := CheckFolderChangeable(folders, "inbox"); !errors.Is(err, ErrFolderProtected) {
		t.Errorf("INBOX no distingue mayusculas: %v", err)
	}
	// Un padre que contiene una carpeta con papel tampoco: renombrarlo la arrastraria.
	if _, err := CheckFolderChangeable(folders, "Proyectos"); !errors.Is(err, ErrFolderProtected) {
		t.Errorf("padre de una carpeta protegida: %v", err)
	}
	if _, err := CheckFolderChangeable(folders, "NoExiste"); !errors.Is(err, ErrFolderNotFound) {
		t.Errorf("carpeta inexistente: %v", err)
	}
	if f, err := CheckFolderChangeable(folders, "Clientes"); err != nil || f.Name != "Clientes" {
		t.Errorf("carpeta propia: %+v %v", f, err)
	}
	if kids := Descendants(folders, folders[3]); len(kids) != 1 || kids[0].Name != "Clientes/2026" {
		t.Errorf("subcarpetas: %+v", kids)
	}
}

func TestNombreNuevoDeCarpeta(t *testing.T) {
	var verr *ValidationError
	for _, bad := range []string{"", "INBOX", "inbox", "Scheduled", " Espacio", "/Clientes", "Clientes/", "A//B", "A/../B", "A/./B", "Mal*", "Con\nsalto"} {
		if err := ValidateNewFolderName(bad, "/"); !errors.As(err, &verr) || verr.Field != "name" {
			t.Errorf("%q: %v", bad, err)
		}
	}
	for _, ok := range []string{"Clientes", "Clientes/2026", "Facturas 2026", "Año"} {
		if err := ValidateNewFolderName(ok, "/"); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

func TestDestinoDeUnRenombrado(t *testing.T) {
	folders := sampleFolders()
	clientes := folders[3]
	if err := CheckRenameTarget(folders, clientes, "Proyectos"); !errors.Is(err, ErrFolderExists) {
		t.Errorf("nombre existente: %v", err)
	}
	var verr *ValidationError
	for _, bad := range []string{"Clientes", "Clientes/Nueva", "INBOX"} {
		if err := CheckRenameTarget(folders, clientes, bad); !errors.As(err, &verr) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := CheckRenameTarget(folders, clientes, "Cartera"); err != nil {
		t.Errorf("nombre libre: %v", err)
	}
}

func TestSoloSeVacianPapeleraYSpam(t *testing.T) {
	for _, role := range []FolderRole{RoleTrash, RoleJunk} {
		if err := CheckEmptiable(Folder{Role: role}); err != nil {
			t.Errorf("%s: %v", role, err)
		}
	}
	for _, role := range []FolderRole{RoleInbox, RoleSent, RoleNone, RoleScheduled} {
		if err := CheckEmptiable(Folder{Role: role}); !errors.Is(err, ErrFolderNotEmptiable) {
			t.Errorf("%q: %v", role, err)
		}
	}
}

func TestLote(t *testing.T) {
	b, err := NewBatch("INBOX", []uint32{3, 1, 3}, "flags", []string{`\seen`}, nil, "")
	if err != nil || len(b.UIDs) != 2 || b.Flags.Add[0] != FlagSeen {
		t.Fatalf("flags: %+v %v", b, err)
	}
	if b, err = NewBatch("INBOX", []uint32{1}, "move", nil, nil, "Archive"); err != nil || b.To != "Archive" {
		t.Fatalf("move: %+v %v", b, err)
	}
	if _, err = NewBatch("INBOX", []uint32{1}, "delete", nil, nil, ""); err != nil {
		t.Fatalf("delete: %v", err)
	}
	tooMany := make([]uint32, MaxBatchUIDs+1)
	for i := range tooMany {
		tooMany[i] = uint32(i + 1)
	}
	cases := map[string]struct {
		uids   []uint32
		action string
		to     string
		add    []string
		field  string
	}{
		"vacio":         {nil, "delete", "", nil, "uids"},
		"uid cero":      {[]uint32{0}, "delete", "", nil, "uids"},
		"demasiados":    {tooMany, "delete", "", nil, "uids"},
		"accion":        {[]uint32{1}, "purge", "", nil, "action"},
		"misma carpeta": {[]uint32{1}, "move", "INBOX", nil, "to"},
		"destino malo":  {[]uint32{1}, "move", "A\r\nB", nil, "to"},
		"flag malo":     {[]uint32{1}, "flags", "", []string{`\Deleted`}, "add"},
	}
	for name, c := range cases {
		var verr *ValidationError
		if _, err := NewBatch("INBOX", c.uids, c.action, c.add, nil, c.to); !errors.As(err, &verr) || verr.Field != c.field {
			t.Errorf("%s: %v", name, err)
		}
	}
	full := tooMany[:MaxBatchUIDs]
	if _, err := NewBatch("INBOX", full, "delete", nil, nil, ""); err != nil {
		t.Errorf("el tope exacto se admite: %v", err)
	}
}

func TestBusquedaAvanzada(t *testing.T) {
	q, _ := NewListQuery(1, 10, "")
	since, _ := ParseSearchDate("since", "2026-09-01")
	before, _ := ParseSearchDate("before", "2026-09-30")
	got, err := q.WithFilter(SearchFilter{From: "  ana@x.test ", Subject: "Factura", Since: since, Before: before, Unread: true, HasAttachments: true})
	if err != nil || got.Filter.From != "ana@x.test" || !got.Filter.Unread || got.Filter.Since.Day() != 1 {
		t.Fatalf("%+v %v", got.Filter, err)
	}
	var verr *ValidationError
	if _, err := ParseSearchDate("since", "01/09/2026"); !errors.As(err, &verr) || verr.Field != "since" {
		t.Errorf("fecha mal formada: %v", err)
	}
	if _, err := q.WithFilter(SearchFilter{Since: before, Before: since}); !errors.As(err, &verr) || verr.Field != "before" {
		t.Errorf("before antes que since: %v", err)
	}
	if _, err := q.WithFilter(SearchFilter{To: strings.Repeat("a", MaxSearchBytes+1)}); !errors.As(err, &verr) || verr.Field != "to" {
		t.Errorf("texto largo: %v", err)
	}
	if _, err := q.WithFilter(SearchFilter{Subject: "a\x00b"}); !errors.As(err, &verr) || verr.Field != "subject" {
		t.Errorf("control: %v", err)
	}
}

func TestHoraDeEnvioProgramado(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	var verr *ValidationError
	for name, at := range map[string]time.Time{
		"vacia":       {},
		"pasado":      now.Add(-time.Hour),
		"muy pronto":  now.Add(30 * time.Second),
		"muy lejos":   now.AddDate(0, 0, 31),
		"justo fuera": now.AddDate(0, 0, 30).Add(time.Second),
	} {
		if err := ValidateSendAt(at, now, 30); !errors.As(err, &verr) || verr.Field != "send_at" {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, at := range map[string]time.Time{"un minuto": now.Add(MinScheduleLead), "tope": now.AddDate(0, 0, 30)} {
		if err := ValidateSendAt(at, now, 30); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if at, err := ParseSendAt("2026-09-24T12:00:00-05:00"); err != nil || at.Hour() != 17 || at.Location() != time.UTC {
		t.Errorf("RFC 3339 con zona: %v %v", at, err)
	}
	if _, err := ParseSendAt("manana"); !errors.As(err, &verr) {
		t.Errorf("hora ilegible: %v", err)
	}
	if ValidateScheduledID("00000000-0000-4000-8000-000000000001") != nil || ValidateScheduledID("../x") == nil {
		t.Error("id de envio programado")
	}
}

func TestSesionSinEmpresaNiBuzon(t *testing.T) {
	if _, ok := (Session{Username: "a@b.pe"}).Mailbox(); ok {
		t.Fatal("una sesion anterior sin empresa ni buzon no abre mail-dav")
	}
	s := SessionPolicy{Idle: time.Minute, Max: time.Hour}.Open(Identity{
		Username: "a@b.pe", TenantID: "11111111-1111-4111-8111-111111111111", MailboxID: "22222222-2222-4222-8222-222222222222",
	}, time.Now())
	mb, ok := s.Mailbox()
	if !ok || mb.TenantID != "11111111-1111-4111-8111-111111111111" || mb.MailboxID != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("%+v %v", mb, ok)
	}
}

func TestIdentificadoresDeLibretaYCalendario(t *testing.T) {
	for _, ok := range []string{"0b6f7c1e-1a2b-4c3d-8e9f-0a1b2c3d4e5f", "contacto@ejemplo.test", "a.b_c~d=e+f-g"} {
		if err := ValidateResourceID(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", "a%2Fb", "a b", strings.Repeat("a", 256)} {
		if err := ValidateResourceID(bad); err == nil {
			t.Errorf("%q aceptado", bad)
		}
	}
	if ValidateIfMatch(`"abc"`) != nil || ValidateIfMatch("") != nil || ValidateIfMatch("\"a\"\r\nX: y") == nil {
		t.Error("If-Match")
	}
	if _, err := NewEventWindow("2026-09-01T00:00:00Z", "2026-09-01T00:00:00Z"); err == nil {
		t.Error("ventana vacia")
	}
	if _, err := NewEventWindow("ayer", "2026-09-01T00:00:00Z"); err == nil {
		t.Error("ventana ilegible")
	}
	if w, err := NewEventWindow("2026-09-01T00:00:00Z", "2026-10-01T00:00:00-05:00"); err != nil || !w.End.After(w.Start) {
		t.Errorf("ventana: %+v %v", w, err)
	}
}
