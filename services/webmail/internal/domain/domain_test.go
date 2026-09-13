package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNormalizeUsername(t *testing.T) {
	valid := map[string]string{
		"Ana@Empresa.PE":       "ana@empresa.pe",
		"  luis.p+tag@x.com  ": "luis.p+tag@x.com",
	}
	for in, want := range valid {
		if got, ok := NormalizeUsername(in); !ok || got != want {
			t.Errorf("%q: got %q ok=%v", in, got, ok)
		}
	}
	// '*' es el separador de usuario maestro de Dovecot: nunca puede formar parte del nombre.
	for _, in := range []string{"", "ana", "ana@", "@x.com", "ana@x.com*webmail@platform.local",
		"ana*webmail@platform.local", "ana @x.com", "ana@x.com\r\nA1 LOGOUT", `ana"@x.com`} {
		if _, ok := NormalizeUsername(in); ok {
			t.Errorf("%q no debe ser un nombre de buzon valido", in)
		}
	}
}

func TestSessionPolicyInactividadYVidaMaxima(t *testing.T) {
	p := SessionPolicy{Idle: 30 * time.Minute, Max: 12 * time.Hour}
	start := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)
	s := p.Open(Identity{Username: "ana@x.com"}, start)
	if !s.ExpiresAt.Equal(start.Add(12 * time.Hour)) {
		t.Fatalf("vida maxima: %v", s.ExpiresAt)
	}
	if ttl, ok := p.Remaining(s, start); !ok || ttl != 30*time.Minute {
		t.Fatalf("al abrir: ttl=%v ok=%v", ttl, ok)
	}
	if ttl, ok := p.Remaining(s, start.Add(12*time.Hour-10*time.Minute)); !ok || ttl != 10*time.Minute {
		t.Fatalf("cerca del maximo la inactividad se recorta: ttl=%v ok=%v", ttl, ok)
	}
	if _, ok := p.Remaining(s, start.Add(12*time.Hour)); ok {
		t.Fatal("al llegar a la vida maxima la sesion caduca")
	}
}

func TestSessionPolicyValidate(t *testing.T) {
	for _, p := range []SessionPolicy{{0, time.Hour}, {time.Hour, 0}, {2 * time.Hour, time.Hour}} {
		if p.Validate() == nil {
			t.Errorf("%+v debe ser invalida", p)
		}
	}
	if err := (SessionPolicy{30 * time.Minute, 12 * time.Hour}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRevokedBy(t *testing.T) {
	t0 := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)
	s := Session{CreatedAt: t0}
	if s.RevokedBy(time.Time{}) {
		t.Fatal("sin revocacion no se revoca")
	}
	if !s.RevokedBy(t0) || !s.RevokedBy(t0.Add(time.Second)) {
		t.Fatal("una revocacion en o despues del inicio la alcanza")
	}
	if s.RevokedBy(t0.Add(-time.Second)) {
		t.Fatal("una revocacion anterior no alcanza a una sesion abierta despues")
	}
}

func TestValidateFolderName(t *testing.T) {
	for _, ok := range []string{"INBOX", "INBOX/Proyectos", "Papelería", "con espacio"} {
		if err := ValidateFolderName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	bad := []string{"", "INBOX\r\nA001 DELETE INBOX", "a\x00b", "a\x7fb", "*", "Sub%",
		strings.Repeat("a", MaxFolderNameBytes+1), string([]byte{0xff, 0xfe})}
	for _, name := range bad {
		var verr *ValidationError
		if err := ValidateFolderName(name); !errors.As(err, &verr) {
			t.Errorf("%q debe rechazarse con un error de validacion, dio %v", name, err)
		}
	}
}

func TestValidateHeaderTextRechazaCRLF(t *testing.T) {
	for _, v := range []string{"Hola\r\nBcc: espia@x.com", "Hola\nBcc: x@y.com", "Hola\rX", "a\x00b", "a\x1bb"} {
		if ValidateHeaderText("subject", v, 1000) == nil {
			t.Errorf("%q debe rechazarse", v)
		}
	}
	if err := ValidateHeaderText("subject", "Con\ttabulador y acentos áéí", 1000); err != nil {
		t.Fatal(err)
	}
	if ValidateHeaderText("subject", strings.Repeat("a", 11), 10) == nil {
		t.Fatal("el tope de longitud se aplica")
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":         "passwd",
		`C:\Users\ana\informe.pdf`: "informe.pdf",
		"factura\u202Efdp.exe":     "facturafdp.exe",
		"nota\r\n.txt":             "nota.txt",
		`"comillas".txt`:           "_comillas_.txt",
		"a:b|c?.txt":               "a_b_c_.txt",
		"...":                      "adjunto",
		"":                         "adjunto",
		"  .oculto  ":              "oculto",
	}
	for in, want := range cases {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	long := SanitizeFilename(strings.Repeat("ñ", 150) + ".pdf")
	if len(long) > maxFilenameBytes || !utf8.ValidString(long) || !strings.HasSuffix(long, ".pdf") {
		t.Fatalf("recorte invalido: %d bytes, %q", len(long), long)
	}
}

func TestParseAddressField(t *testing.T) {
	got, err := ParseAddressField("to", []string{`"Pérez, Ana" <ana@Empresa.PE>, luis@x.com`, "", "  "})
	if err != nil {
		t.Fatal(err)
	}
	want := []Address{{Name: "Pérez, Ana", Email: "ana@empresa.pe"}, {Email: "luis@x.com"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v", got)
	}
	for _, bad := range []string{"ana@x.com\r\nBcc: espia@x.com", "no-es-direccion", "ñandú@x.com", "ana@localhost"} {
		if _, err := ParseAddressField("to", []string{bad}); err == nil {
			t.Errorf("%q debe rechazarse", bad)
		}
	}
}

func TestDraftLimites(t *testing.T) {
	l := Limits{MaxRecipients: 2, MaxMessageBytes: 100}
	base := Draft{From: Address{Email: "ana@x.com"}, To: []Address{{Email: "a@x.com"}}}
	if err := base.ValidateForSend(l); err != nil {
		t.Fatal(err)
	}

	d := base
	d.Cc = []Address{{Email: "A@x.com"}, {Email: "b@x.com"}}
	if err := d.ValidateForSend(l); err != nil {
		t.Fatalf("los duplicados cuentan una vez: %v", err)
	}
	d.Bcc = []Address{{Email: "c@x.com"}}
	if err := d.ValidateForSend(l); !errors.Is(err, ErrTooManyRecipients) {
		t.Fatalf("Bcc cuenta en el tope: %v", err)
	}

	big := base
	big.Text = strings.Repeat("x", 101)
	if err := big.ValidateForSend(l); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("texto: %v", err)
	}
	att := base
	att.Attachments = []Attachment{{Filename: "a.bin", Data: make([]byte, 101)}}
	if err := att.ValidateForSend(l); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("adjunto: %v", err)
	}

	none := Draft{From: Address{Email: "ana@x.com"}}
	var verr *ValidationError
	if err := none.ValidateForSend(l); !errors.As(err, &verr) || verr.Field != "to" {
		t.Fatalf("sin destinatarios no se envia: %v", err)
	}
	if err := none.ValidateForSave(l); err != nil {
		t.Fatalf("un borrador puede no tener destinatarios: %v", err)
	}

	crlf := base
	crlf.Subject = "hola\r\nBcc: espia@x.com"
	if err := crlf.ValidateForSend(l); !errors.As(err, &verr) || verr.Field != "subject" {
		t.Fatalf("asunto con CRLF: %v", err)
	}

	many := base
	many.Attachments = make([]Attachment, MaxAttachments+1)
	if err := many.ValidateForSend(Limits{MaxRecipients: 2, MaxMessageBytes: 1 << 20}); !errors.As(err, &verr) {
		t.Fatalf("demasiados adjuntos: %v", err)
	}
}

func TestNewFlagChange(t *testing.T) {
	c, err := NewFlagChange([]string{`\seen`, `\Seen`, `\Flagged`}, []string{`\Answered`})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Add) != 2 || c.Add[0] != FlagSeen || c.Add[1] != FlagFlagged || len(c.Remove) != 1 || c.Remove[0] != FlagAnswered {
		t.Fatalf("got %+v", c)
	}
	for _, bad := range [][2][]string{
		{{`\Deleted`}, nil},
		{{`\Draft`}, nil},
		{{"$Custom"}, nil},
		{{`\Seen`}, {`\Seen`}},
		{nil, nil},
	} {
		if _, err := NewFlagChange(bad[0], bad[1]); err == nil {
			t.Errorf("%v debe rechazarse", bad)
		}
	}
}

func TestParsePartIDYUID(t *testing.T) {
	if p, err := ParsePartID("1.2.3"); err != nil || len(p) != 3 || p[2] != 3 || FormatPartID(p) != "1.2.3" {
		t.Fatalf("got %v %v", p, err)
	}
	for _, bad := range []string{"", "0", "1.", ".1", "1..2", "a", "1.0", "99999", "1.2.3.4.5.6.7.8.9.10.11"} {
		if _, err := ParsePartID(bad); err == nil {
			t.Errorf("parte %q debe rechazarse", bad)
		}
	}
	if u, err := ParseUID("42"); err != nil || u != 42 {
		t.Fatalf("uid: %v %v", u, err)
	}
	for _, bad := range []string{"", "0", "-1", "+3", "4294967296", "1e3"} {
		if _, err := ParseUID(bad); err == nil {
			t.Errorf("uid %q debe rechazarse", bad)
		}
	}
}

func TestSafeDownloadType(t *testing.T) {
	cases := map[string]string{
		"image/PNG":                      "image/png",
		"application/pdf; name=x.pdf":    "application/pdf",
		"text/html":                      "application/octet-stream",
		"image/svg+xml":                  "application/octet-stream",
		"application/x-msdownload":       "application/octet-stream",
		"no es un tipo":                  "application/octet-stream",
		"text/plain; charset=iso-8859-1": "text/plain",
	}
	for in, want := range cases {
		if got := SafeDownloadType(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestIsValidMessageID(t *testing.T) {
	if !IsValidMessageID("abc.123@empresa.pe") {
		t.Fatal("identificador valido rechazado")
	}
	for _, bad := range []string{"", "a b@x", "a>b@x", "<a@x>", "a\r\nb@x", strings.Repeat("a", 251)} {
		if IsValidMessageID(bad) {
			t.Errorf("%q debe rechazarse", bad)
		}
	}
}

func TestNewListQuery(t *testing.T) {
	q, err := NewListQuery(0, 0, "  factura ")
	if err != nil || q.Page != 1 || q.PerPage != DefaultPerPage || q.Search != "factura" {
		t.Fatalf("got %+v %v", q, err)
	}
	if q, _ := NewListQuery(1, 1000, ""); q.PerPage != MaxPerPage {
		t.Fatalf("per_page se acota: %d", q.PerPage)
	}
	if _, err := NewListQuery(1, 10, "a\r\nb"); err == nil {
		t.Fatal("una busqueda con saltos de linea se rechaza")
	}
	q, _ = NewListQuery(3, 10, "")
	if s, e := q.Window(25); s != 20 || e != 25 {
		t.Fatalf("ventana: %d-%d", s, e)
	}
	if s, e := q.Window(5); s != 5 || e != 5 {
		t.Fatalf("pagina fuera de rango: %d-%d", s, e)
	}
}

func TestFolderWithRoleExigeSeleccionable(t *testing.T) {
	folders := []Folder{{Name: "Trash", Role: RoleTrash}, {Name: "Papelera", Role: RoleTrash, Selectable: true}}
	if f, ok := FolderWithRole(folders, RoleTrash); !ok || f.Name != "Papelera" {
		t.Fatalf("got %+v %v", f, ok)
	}
	if RoleByName("inbox") != RoleInbox || RoleByName("Sent Items") != RoleSent || RoleByName("Proyectos") != RoleNone {
		t.Fatal("respaldo por nombre")
	}
}
