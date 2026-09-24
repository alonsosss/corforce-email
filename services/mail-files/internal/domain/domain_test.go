package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

func TestSafeFileNameNeutralizaNombresHostiles(t *testing.T) {
	cases := []struct {
		raw, want string
	}{
		{"contrato.pdf", "contrato.pdf"},
		{"../../etc/passwd", "passwd"},
		{`C:\Users\ana\planos final.dwg`, "planos final.dwg"},
		{"factura\u202Efdp.exe", "facturafdp.exe"},
		{"nombre\x00con\x1fcontrol.txt", "nombreconcontrol.txt"},
		{"  ..oculto.txt.. ", "oculto.txt"},
		{`a<b>c:d"e|f?g*h.zip`, "a_b_c_d_e_f_g_h.zip"},
		{"varios   \t espacios.doc", "varios espacios.doc"},
		{"CON.txt", "_CON.txt"},
		{"lpt9", "_lpt9"},
		{"cero\u200Bancho.png", "ceroancho.png"},
		{"presupuesto año 2026.xlsx", "presupuesto año 2026.xlsx"},
		{"\xff\xfeinvalido.bin", "_invalido.bin"},
	}
	for _, c := range cases {
		got, err := SafeFileName(c.raw)
		if err != nil || got != c.want {
			t.Errorf("SafeFileName(%q) = %q, %v; se esperaba %q", c.raw, got, err, c.want)
		}
	}
}

func TestSafeFileNameRechazaLoQueNoDejaNombre(t *testing.T) {
	for _, raw := range []string{"", "   ", "../", "..", "\u202E\u200B", "/", `\\`} {
		var verr *ValidationError
		if _, err := SafeFileName(raw); !errors.As(err, &verr) || verr.Field != "name" {
			t.Errorf("SafeFileName(%q): %v", raw, err)
		}
	}
}

func TestSafeFileNameRecortaConservandoLaExtensionYLosCaracteres(t *testing.T) {
	long := strings.Repeat("ñ", 300) + ".pdf"
	got, err := SafeFileName(long)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > MaxFileNameBytes || !strings.HasSuffix(got, ".pdf") || !utf8.ValidString(got) {
		t.Fatalf("recorte: %d bytes, %q", len(got), got[len(got)-10:])
	}
	noExt := strings.Repeat("x", 500)
	if got, _ := SafeFileName(noExt); len(got) != MaxFileNameBytes {
		t.Fatalf("sin extension: %d", len(got))
	}
}

func TestNombresParaContentDisposition(t *testing.T) {
	name := `año "final" 100%.pdf`
	if got := ASCIIFileName(name); got != `a_o _final_ 100_.pdf` {
		t.Fatalf("ascii: %q", got)
	}
	if got := RFC5987(name); got != "a%C3%B1o%20%22final%22%20100%25.pdf" {
		t.Fatalf("rfc5987: %q", got)
	}
}

func signer(t *testing.T) *LinkSigner {
	t.Helper()
	s, err := NewLinkSigner(strings.Repeat("k", 32), "https://correo.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEnlaceFirmadoSeVerificaYDetectaCualquierCambio(t *testing.T) {
	s := signer(t)
	c := LinkClaims{TenantID: uuid.New(), FileID: uuid.New(), ExpiresAt: time.Unix(1_900_000_000, 0)}
	sig := s.Sign(c)
	if !s.Verify(c, sig) {
		t.Fatal("la firma propia no verifica")
	}
	for name, altered := range map[string]LinkClaims{
		"empresa":   {TenantID: uuid.New(), FileID: c.FileID, ExpiresAt: c.ExpiresAt},
		"fichero":   {TenantID: c.TenantID, FileID: uuid.New(), ExpiresAt: c.ExpiresAt},
		"caducidad": {TenantID: c.TenantID, FileID: c.FileID, ExpiresAt: c.ExpiresAt.Add(time.Second)},
	} {
		if s.Verify(altered, sig) {
			t.Errorf("una firma vale con otra %s", name)
		}
	}
	flipped := "0"
	if strings.HasSuffix(sig, "0") {
		flipped = "1"
	}
	if s.Verify(c, sig[:len(sig)-1]+flipped) {
		t.Error("una firma alterada verifica")
	}
	if s.Verify(c, "") || s.Verify(c, sig[:10]) {
		t.Error("una firma corta verifica")
	}
	other, _ := NewLinkSigner(strings.Repeat("z", 32), "https://correo.example.com")
	if other.Verify(c, sig) {
		t.Error("la firma de otra clave verifica")
	}
}

func TestLaURLDelEnlaceSeLeeConParseLink(t *testing.T) {
	s := signer(t)
	c := LinkClaims{TenantID: uuid.New(), FileID: uuid.New(), ExpiresAt: time.Unix(1_900_000_000, 0)}
	raw := s.URL(c)
	prefix := "https://correo.example.com" + DownloadPath + "/" + c.TenantID.String() + "/" + c.FileID.String() + "?"
	if !strings.HasPrefix(raw, prefix) {
		t.Fatalf("url: %s", raw)
	}
	query := strings.TrimPrefix(raw, prefix)
	parts := map[string]string{}
	for _, kv := range strings.Split(query, "&") {
		k, v, _ := strings.Cut(kv, "=")
		parts[k] = v
	}
	parsed, sig, err := ParseLink(c.TenantID.String(), c.FileID.String(), parts["x"], parts["s"])
	if err != nil || parsed != c || !s.Verify(parsed, sig) {
		t.Fatalf("lectura: %+v %v", parsed, err)
	}
}

func TestParseLinkRechazaDatosIlegibles(t *testing.T) {
	good := strings.Repeat("a", 64)
	tenant, file := uuid.NewString(), uuid.NewString()
	for _, c := range [][4]string{
		{"x", file, "1", good},
		{tenant, "../etc", "1", good},
		{uuid.Nil.String(), file, "1", good},
		{tenant, file, "-1", good},
		{tenant, file, "manana", good},
		{tenant, file, "1", "corta"},
	} {
		if _, _, err := ParseLink(c[0], c[1], c[2], c[3]); !errors.Is(err, ErrLinkInvalid) {
			t.Errorf("ParseLink(%v): %v", c, err)
		}
	}
}

func TestNewLinkSignerExigeClaveYBase(t *testing.T) {
	if _, err := NewLinkSigner("corta", "https://x.example"); err == nil {
		t.Error("clave corta aceptada")
	}
	for _, base := range []string{"", "correo.example.com", "ftp://x.example", "https://u:p@x.example", "https://x.example/?a=1"} {
		if _, err := NewLinkSigner(strings.Repeat("k", 32), base); err == nil {
			t.Errorf("base %q aceptada", base)
		}
	}
}

func policy() Policy {
	return Policy{MaxFileBytes: 100, DefaultExpiryDays: 7, MaxExpiryDays: 30, DefaultMaxDownloads: 5, MaxDownloads: 10,
		MailboxQuotaBytes: 300, TenantQuotaBytes: 500, MaxActivePerMailbox: 3}
}

func TestPolicyValidate(t *testing.T) {
	if err := policy().Validate(); err != nil {
		t.Fatal(err)
	}
	broken := []func(*Policy){
		func(p *Policy) { p.MaxFileBytes = 0 },
		func(p *Policy) { p.DefaultExpiryDays = 31 },
		func(p *Policy) { p.DefaultMaxDownloads = 0 },
		func(p *Policy) { p.MailboxQuotaBytes = 50 },
		func(p *Policy) { p.TenantQuotaBytes = 200 },
		func(p *Policy) { p.MaxActivePerMailbox = 0 },
	}
	for i, mutate := range broken {
		p := policy()
		mutate(&p)
		if p.Validate() == nil {
			t.Errorf("politica %d aceptada", i)
		}
	}
}

func TestPolicyResolveAplicaDefectosYTopes(t *testing.T) {
	p := policy()
	in, dl, err := p.Resolve(UploadOptions{})
	if err != nil || in != 7*24*time.Hour || dl != 5 {
		t.Fatalf("defectos: %v %d %v", in, dl, err)
	}
	if in, dl, err = p.Resolve(UploadOptions{ExpiresInDays: 30, MaxDownloads: 10}); err != nil || in != 30*24*time.Hour || dl != 10 {
		t.Fatalf("topes: %v %d %v", in, dl, err)
	}
	var verr *ValidationError
	if _, _, err := p.Resolve(UploadOptions{ExpiresInDays: 31}); !errors.As(err, &verr) || verr.Field != "expires_in_days" {
		t.Fatalf("caducidad excesiva: %v", err)
	}
	if _, _, err := p.Resolve(UploadOptions{MaxDownloads: 11}); !errors.As(err, &verr) || verr.Field != "max_downloads" {
		t.Fatalf("descargas excesivas: %v", err)
	}
	if _, _, err := p.Resolve(UploadOptions{MaxDownloads: -1}); err == nil {
		t.Fatal("descargas negativas aceptadas")
	}
}

func TestPolicyAdmitsNombraLaCuotaSuperada(t *testing.T) {
	p := policy()
	if err := p.Admits(Usage{MailboxBytes: 200, MailboxActive: 2, TenantBytes: 200}, 100); err != nil {
		t.Fatalf("cabe justo: %v", err)
	}
	if err := p.Admits(Usage{MailboxBytes: 201, MailboxActive: 2}, 100); !errors.Is(err, ErrMailboxQuota) {
		t.Fatalf("buzon: %v", err)
	}
	if err := p.Admits(Usage{MailboxBytes: 0, TenantBytes: 401}, 100); !errors.Is(err, ErrTenantQuota) {
		t.Fatalf("empresa: %v", err)
	}
	if err := p.Admits(Usage{MailboxActive: 3}, 1); !errors.Is(err, ErrTooManyFiles) {
		t.Fatalf("enlaces: %v", err)
	}
}

func TestEstadoDeducidoDeLaHoraYLasDescargas(t *testing.T) {
	now := time.Now()
	f := File{Status: StatusReady, ExpiresAt: now.Add(time.Hour), MaxDownloads: 2, Downloads: 1}
	if f.State(now) != StateActive || !f.Downloadable(now) || f.RemainingDownloads() != 1 {
		t.Fatalf("activo: %s", f.State(now))
	}
	if f.State(now.Add(time.Hour)) != StateExpired || f.Downloadable(now.Add(time.Hour)) {
		t.Fatal("caducado a su hora exacta")
	}
	f.Downloads = 2
	if f.State(now) != StateExhausted || f.Downloadable(now) {
		t.Fatal("agotado")
	}
	f.Downloads, f.ObjectDeleted = 0, true
	if f.Downloadable(now) {
		t.Fatal("sin objeto no se descarga")
	}
	for status, want := range map[Status]State{StatusPending: StateUploading, StatusRevoked: StateRevoked, StatusFailed: StateFailed, StatusExpired: StateExpired} {
		if got := (File{Status: status, ExpiresAt: now.Add(time.Hour), MaxDownloads: 1}).State(now); got != want {
			t.Errorf("%s: %s", status, got)
		}
	}
}

func TestLaClaveDelObjetoEsPrivadaYNoLlevaElNombre(t *testing.T) {
	tenant, file := uuid.New(), uuid.New()
	key := ObjectKey(tenant, file)
	if key != "private/"+tenant.String()+"/mail-files/"+file.String() {
		t.Fatalf("clave: %s", key)
	}
}
