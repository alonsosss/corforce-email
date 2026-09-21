package domain

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestIsPublicAddrRechazaLoInterno(t *testing.T) {
	casos := []struct {
		addr   string
		public bool
	}{
		{"8.8.8.8", true},
		{"93.184.216.34", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"127.255.255.254", false},
		{"10.0.0.1", false},
		{"172.16.0.1", false},
		{"172.31.255.255", false},
		{"172.32.0.1", true},
		{"192.168.1.10", false},
		{"100.64.0.1", false},
		{"169.254.169.254", false},
		{"169.254.0.1", false},
		{"0.0.0.0", false},
		{"224.0.0.1", false},
		{"255.255.255.255", false},
		{"192.0.2.10", false},
		{"198.18.0.1", false},
		{"::1", false},
		{"::", false},
		{"fe80::1", false},
		{"fd00::1", false},
		{"fd00:ec2::254", false},
		{"fc00::1", false},
		{"ff02::1", false},
		{"2001:db8::1", false},
		{"2002:c000:204::1", false},
		{"64:ff9b::a00:1", false},
		{"::ffff:10.0.0.1", false},
		{"::ffff:127.0.0.1", false},
		{"::ffff:8.8.8.8", true},
	}
	for _, c := range casos {
		if got := IsPublicAddr(netip.MustParseAddr(c.addr)); got != c.public {
			t.Errorf("%s: publica=%v, se esperaba %v", c.addr, got, c.public)
		}
	}
	if IsPublicAddr(netip.Addr{}) {
		t.Error("una direccion invalida no es publica")
	}
	if IsPublicAddr(netip.MustParseAddr("fe80::1%eth0")) {
		t.Error("una direccion con zona no es publica")
	}
}

func validSource() Source {
	return Source{Host: "imap.proveedor.example", Port: 993, TLS: TLSImplicit, Username: "ana@proveedor.example", Password: "secreto-largo"}
}

var policy = SourcePolicy{Ports: []int{143, 993}}

func TestNormalizeAceptaUnOrigenValido(t *testing.T) {
	s := validSource()
	s.Host = " IMAP.Proveedor.Example. "
	s.Username = " ana@proveedor.example "
	if err := policy.Normalize(&s); err != nil {
		t.Fatal(err)
	}
	if s.Host != "imap.proveedor.example" || s.Username != "ana@proveedor.example" {
		t.Fatalf("no normalizo: %+v", s)
	}
}

func TestNormalizeRechazaServidoresInternosSinResolver(t *testing.T) {
	for _, host := range []string{
		"127.0.0.1", "10.1.2.3", "192.168.0.5", "169.254.169.254", "::1", "[::1]x", "fe80::1", "fd00:ec2::254",
		"::ffff:10.0.0.1", "0.0.0.0", "100.100.100.200",
	} {
		s := validSource()
		s.Host = host
		err := policy.Normalize(&s)
		if err == nil {
			t.Errorf("%s: se admitio", host)
			continue
		}
		if !errors.Is(err, ErrHostNotAllowed) && !errors.Is(err, ErrInvalidHost) {
			t.Errorf("%s: error inesperado %v", host, err)
		}
		if FieldOf(err) != "source_host" {
			t.Errorf("%s: campo %q", host, FieldOf(err))
		}
	}
}

func TestNormalizeRechazaNombresQueNoSonDNSPublico(t *testing.T) {
	largo := strings.Repeat("a", 64) + ".example"
	for _, host := range []string{
		"", " ", "localhost", "intranet", "imap", "2130706433", "0x7f.0.0.1", "127.1", "a..example", "-a.example", "a-.example",
		"imap.exam ple", "imap.example/x", "imap.example:993", "imap_1.example", "correo.éx.example", "user@imap.example",
		largo, strings.Repeat("a.", 130) + "com", "10.0.0.1.", "1.2.3.4.5",
	} {
		s := validSource()
		s.Host = host
		if err := policy.Normalize(&s); err == nil {
			t.Errorf("%q: se admitio", host)
		}
	}
}

func TestNormalizeAceptaUnaIPPublicaLiteral(t *testing.T) {
	for _, host := range []string{"8.8.8.8", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		s := validSource()
		s.Host = host
		if err := policy.Normalize(&s); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
}

func TestNormalizeAdmiteDireccionesInternasSoloConLaOpcionDePruebas(t *testing.T) {
	p := SourcePolicy{Ports: []int{143, 993}, AllowPrivate: true}
	s := validSource()
	s.Host = "10.0.0.5"
	if err := p.Normalize(&s); err != nil {
		t.Fatalf("con AllowPrivate: %v", err)
	}
	if err := p.CheckAddresses([]netip.Addr{netip.MustParseAddr("10.0.0.5")}); err != nil {
		t.Fatalf("CheckAddresses con AllowPrivate: %v", err)
	}
	if err := policy.CheckAddresses([]netip.Addr{netip.MustParseAddr("10.0.0.5")}); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("por defecto debe rechazarla: %v", err)
	}
}

func TestNormalizePuertoYTLS(t *testing.T) {
	casos := []struct {
		name string
		mut  func(*Source)
		want error
	}{
		{"puerto smtp", func(s *Source) { s.Port = 25 }, ErrInvalidPort},
		{"puerto cero", func(s *Source) { s.Port = 0 }, ErrInvalidPort},
		{"puerto negativo", func(s *Source) { s.Port = -1 }, ErrInvalidPort},
		{"tls vacio", func(s *Source) { s.TLS = "" }, ErrInvalidTLS},
		{"tls desconocido", func(s *Source) { s.TLS = "tls1" }, ErrInvalidTLS},
		{"sin tls no permitido", func(s *Source) { s.TLS = TLSNone }, ErrInvalidTLS},
		{"usuario vacio", func(s *Source) { s.Username = "  " }, ErrInvalidUsername},
		{"usuario con salto", func(s *Source) { s.Username = "a\nb" }, ErrInvalidUsername},
		{"usuario enorme", func(s *Source) { s.Username = strings.Repeat("u", 321) }, ErrInvalidUsername},
		{"contrasena vacia", func(s *Source) { s.Password = "" }, ErrInvalidPassword},
		{"contrasena con salto", func(s *Source) { s.Password = "abc\ndef" }, ErrInvalidPassword},
		{"contrasena con NUL", func(s *Source) { s.Password = "abc\x00" }, ErrInvalidPassword},
		{"contrasena enorme", func(s *Source) { s.Password = strings.Repeat("p", 1025) }, ErrInvalidPassword},
		{"contrasena no utf8", func(s *Source) { s.Password = "abc\xff" }, ErrInvalidPassword},
	}
	for _, c := range casos {
		s := validSource()
		c.mut(&s)
		if err := policy.Normalize(&s); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, se esperaba %v", c.name, err, c.want)
		}
	}
	plain := SourcePolicy{Ports: []int{143}, AllowPlaintext: true}
	s := validSource()
	s.Port, s.TLS = 143, TLSNone
	if err := plain.Normalize(&s); err != nil {
		t.Fatalf("sin TLS permitido: %v", err)
	}
	if got := plain.TLSModes(); len(got) != 3 || got[2] != TLSNone {
		t.Fatalf("modos: %v", got)
	}
	if got := policy.TLSModes(); len(got) != 2 {
		t.Fatalf("modos por defecto: %v", got)
	}
}

func TestCheckAddresses(t *testing.T) {
	pub, priv := netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.1")
	if err := policy.CheckAddresses(nil); !errors.Is(err, ErrHostUnresolvable) {
		t.Errorf("sin direcciones: %v", err)
	}
	if err := policy.CheckAddresses([]netip.Addr{pub}); err != nil {
		t.Errorf("publica: %v", err)
	}
	if err := policy.CheckAddresses([]netip.Addr{pub, priv}); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("mezcla de publica y privada debe rechazarse: %v", err)
	}
}

func TestDefaultTLS(t *testing.T) {
	if DefaultTLS(993) != TLSImplicit || DefaultTLS(143) != TLSStartTLS {
		t.Fatal("el modo por puerto no es el esperado")
	}
}

func TestNewJobErrorSaneaElMensaje(t *testing.T) {
	e := NewJobError(CodeSourceAuthFailed, "  fallo\x00 de login para clave hunter2\nlinea dos\t", "hunter2", "")
	if strings.Contains(e.Message, "hunter2") || strings.ContainsAny(e.Message, "\n\x00\t") {
		t.Fatalf("mensaje sin sanear: %q", e.Message)
	}
	if !strings.Contains(e.Message, "***") {
		t.Fatalf("no marca lo retirado: %q", e.Message)
	}
	long := NewJobError(CodeTimeout, strings.Repeat("x", 1000))
	if len([]rune(long.Message)) != maxErrorMessageRunes {
		t.Fatalf("no recorto: %d", len(long.Message))
	}
	if got := NewJobError("inventado", "x").Code; got != CodeImapsyncFailed {
		t.Fatalf("un codigo desconocido debe reducirse: %s", got)
	}
	for _, c := range errorCodes {
		if !c.Valid() {
			t.Errorf("%s no es valido", c)
		}
	}
	if RunnerLostError().Code != CodeRunnerLost || CredentialUnreadableError().Code != CodeCredentialUnreadable {
		t.Fatal("errores del servicio con codigo equivocado")
	}
}

func TestProgressNormalize(t *testing.T) {
	p := Progress{MessagesCopied: 3, Folders: []FolderProgress{{Name: " INBOX\n", MessagesCopied: 3}}}
	if err := p.Normalize(); err != nil {
		t.Fatal(err)
	}
	if p.Folders[0].Name != "INBOX" {
		t.Fatalf("nombre: %q", p.Folders[0].Name)
	}
	empty := Progress{}
	if err := empty.Normalize(); err != nil || empty.Folders == nil {
		t.Fatalf("una lista vacia debe ser [] y no null: %v %v", empty.Folders, err)
	}
	if err := (&Progress{MessagesFailed: -1}).Normalize(); !errors.Is(err, ErrInvalidProgress) {
		t.Errorf("contador negativo: %v", err)
	}
	if err := (&Progress{Folders: []FolderProgress{{Name: "x", MessagesSkipped: -1}}}).Normalize(); !errors.Is(err, ErrInvalidProgress) {
		t.Errorf("carpeta negativa: %v", err)
	}
	many := Progress{Folders: make([]FolderProgress, MaxFolders+50)}
	if err := many.Normalize(); err != nil || len(many.Folders) != MaxFolders {
		t.Errorf("no acoto las carpetas: %d %v", len(many.Folders), err)
	}
	long := Progress{Folders: []FolderProgress{{Name: strings.Repeat("n", 500)}}}
	if err := long.Normalize(); err != nil || len([]rune(long.Folders[0].Name)) != maxFolderNameRunes {
		t.Errorf("no acoto el nombre: %v", err)
	}
}

func TestEstadosFasesYResultados(t *testing.T) {
	for _, s := range Statuses() {
		got, ok := ParseStatus(string(s))
		if !ok || got != s {
			t.Errorf("estado %s", s)
		}
	}
	if _, ok := ParseStatus("otro"); ok {
		t.Error("estado inventado")
	}
	if !StatusPending.Active() || !StatusRunning.Active() || StatusSucceeded.Active() || StatusFailed.Active() || StatusCancelled.Active() {
		t.Error("Active")
	}
	if p, ok := ParsePhase("catchup"); !ok || p != PhaseCatchup {
		t.Error("fase catchup")
	}
	if _, ok := ParsePhase(""); ok {
		t.Error("la fase vacia no es una fase que el ejecutor pueda informar")
	}
	for raw, want := range map[string]Status{"succeeded": StatusSucceeded, "failed": StatusFailed, "cancelled": StatusCancelled} {
		o, err := ParseOutcome(raw)
		if err != nil || o.Status() != want {
			t.Errorf("resultado %s: %v", raw, err)
		}
	}
	if _, err := ParseOutcome("running"); !errors.Is(err, ErrInvalidOutcome) {
		t.Errorf("resultado invalido: %v", err)
	}
}
