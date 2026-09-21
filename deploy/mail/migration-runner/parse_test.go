package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Las muestras de testdata son salidas reales de imapsync 2.314 (Alpine 3.23) contra dos Dovecot
// desechables: origen con TLS implicito o STARTTLS y destino con usuario maestro.
func parseSample(t *testing.T, name string) *outputParser {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	p := newOutputParser()
	// Se escribe en trozos irregulares: el analizador no puede depender de que un trozo sea una linea.
	for len(data) > 0 {
		n := min(137, len(data))
		if _, err := p.Write(data[:n]); err != nil {
			t.Fatal(err)
		}
		data = data[n:]
	}
	return p
}

func TestParseCopiaCompleta(t *testing.T) {
	p := parseSample(t, "imapsync-ok.txt")
	pr := p.Progress()
	if pr.FoldersTotal != 2 || pr.FoldersDone != 2 {
		t.Fatalf("carpetas %d/%d, quiero 2/2", pr.FoldersDone, pr.FoldersTotal)
	}
	if pr.MessagesTotal != 6 || pr.MessagesCopied != 6 || pr.MessagesSkipped != 0 || pr.MessagesFailed != 0 {
		t.Fatalf("mensajes total=%d copiados=%d omitidos=%d fallidos=%d", pr.MessagesTotal, pr.MessagesCopied, pr.MessagesSkipped, pr.MessagesFailed)
	}
	if pr.BytesCopied != 432927 {
		t.Fatalf("bytes copiados %d, quiero 432927", pr.BytesCopied)
	}
	if len(pr.Folders) != 2 || pr.Folders[0].Name != "INBOX" || pr.Folders[0].MessagesCopied != 5 || pr.Folders[1].Name != "Trabajo" || pr.Folders[1].MessagesCopied != 1 {
		t.Fatalf("carpetas: %+v", pr.Folders)
	}
	f := p.Findings()
	if !f.HaveExit || f.Exit != 0 || f.DetectedErrors != 0 || f.NotInDest != 0 {
		t.Fatalf("hallazgos: %+v", f)
	}
	if !classify(f.Exit, f).ok() {
		t.Fatal("una copia completa debe clasificarse como correcta")
	}
}

func TestParseRepasoOmiteLoYaCopiado(t *testing.T) {
	p := parseSample(t, "imapsync-ok-repaso.txt")
	pr := p.Progress()
	if pr.MessagesCopied != 0 || pr.MessagesSkipped != 6 || pr.MessagesFailed != 0 {
		t.Fatalf("copiados=%d omitidos=%d fallidos=%d, quiero 0/6/0", pr.MessagesCopied, pr.MessagesSkipped, pr.MessagesFailed)
	}
	if !classify(p.Findings().Exit, p.Findings()).ok() {
		t.Fatal("el repaso sin novedades debe ser correcto")
	}
}

func TestParseVirusRechazadoPorElFiltro(t *testing.T) {
	p := parseSample(t, "imapsync-virus.txt")
	f := p.Findings()
	if f.Exit != 0 {
		t.Fatalf("imapsync sale 0 aunque el filtro rechace un mensaje: %d", f.Exit)
	}
	if f.ScanInfected != 1 || f.ScanUnavailable != 0 || f.NotInDest != 1 {
		t.Fatalf("hallazgos: %+v", f)
	}
	pr := p.Progress()
	if pr.MessagesCopied != 5 || pr.MessagesFailed != 1 || pr.MessagesSkipped != 0 {
		t.Fatalf("copiados=%d fallidos=%d omitidos=%d, quiero 5/1/0", pr.MessagesCopied, pr.MessagesFailed, pr.MessagesSkipped)
	}
	v := classify(f.Exit, f)
	if v.ok() || v.Err.Code != codeVirusFound || !v.Partial {
		t.Fatalf("un virus con exit 0 debe ser virus_found parcial: %+v", v)
	}
}

func TestParseCuotaExcedida(t *testing.T) {
	p := parseSample(t, "imapsync-cuota.txt")
	f := p.Findings()
	if f.Exit != exitOverQuota || !f.OverQuota {
		t.Fatalf("hallazgos: %+v", f)
	}
	if pr := p.Progress(); pr.MessagesFailed != 1 || pr.MessagesCopied != 0 {
		t.Fatalf("el error del listado final no debe contarse otra vez: copiados=%d fallidos=%d", pr.MessagesCopied, pr.MessagesFailed)
	}
	if v := classify(f.Exit, f); v.ok() || v.Err.Code != codeQuotaExceeded || v.Partial {
		t.Fatalf("veredicto: %+v", v)
	}
}

func TestParseFallosDeConexionYTLS(t *testing.T) {
	cases := []struct {
		file string
		exit int
		code string
	}{
		{"imapsync-auth.txt", exitAuthSource, codeSourceAuthFailed},
		{"imapsync-inaccesible.txt", exitConnectionSource, codeSourceUnreachable},
		{"imapsync-tls-nombre.txt", exitConnectionSource, codeSourceTLSFailed},
		{"imapsync-tls-ca.txt", exitConnectionSource, codeSourceTLSFailed},
		{"imapsync-starttls-nombre.txt", exitTLS, codeSourceTLSFailed},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			p := parseSample(t, c.file)
			f := p.Findings()
			if f.Exit != c.exit {
				t.Fatalf("salida %d, quiero %d", f.Exit, c.exit)
			}
			v := classify(f.Exit, f)
			if v.ok() || v.Err.Code != c.code || v.Partial {
				t.Fatalf("veredicto %+v, quiero %s", v, c.code)
			}
		})
	}
}

func TestMensajesDeErrorNoLlevanContenidoDeLaSalida(t *testing.T) {
	for _, name := range []string{"imapsync-virus.txt", "imapsync-cuota.txt", "imapsync-auth.txt", "imapsync-tls-nombre.txt"} {
		p := parseSample(t, name)
		f := p.Findings()
		v := classify(f.Exit, f)
		if v.ok() {
			continue
		}
		for _, secret := range []string{"alice", "src.test", "grande", "asunto", "Subject", "172.27"} {
			if strings.Contains(v.Err.Message, secret) {
				t.Fatalf("%s: el mensaje %q lleva %q", name, v.Err.Message, secret)
			}
		}
	}
}

func TestParseLineasLargasSeDescartan(t *testing.T) {
	p := newOutputParser()
	long := "msg " + strings.Repeat("a", maxLineBytes*3) + " {10} copied to X/1\n"
	if _, err := p.Write([]byte(long)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("msg INBOX/1 {10}             copied to INBOX/1\n")); err != nil {
		t.Fatal(err)
	}
	if got := p.Progress().MessagesCopied; got != 1 {
		t.Fatalf("copiados %d: la linea desmesurada no debe contar ni romper la siguiente", got)
	}
	if len(p.buf) > maxLineBytes {
		t.Fatalf("el buffer crecio a %d", len(p.buf))
	}
}

func TestParseCarpetaHostilNoAlteraLosContadoresDeManeraPeligrosa(t *testing.T) {
	p := newOutputParser()
	lines := []string{
		"Folder     1/3 [" + strings.Repeat("x", 500) + "\x00\x1b]                     -> [y]",
		"Folder     99999999999999999999/3 [z]   -> [z]",
		"msg A/1 {99999999999999999999} copied to A/1",
	}
	for _, l := range lines {
		_, _ = p.Write([]byte(l + "\n"))
	}
	pr := p.Progress()
	if len([]rune(pr.Folders[0].Name)) > maxFolderName || strings.ContainsAny(pr.Folders[0].Name, "\x00\x1b") {
		t.Fatalf("nombre sin sanear: %q", pr.Folders[0].Name)
	}
	if pr.BytesCopied != 0 {
		t.Fatalf("un tamano fuera de rango no debe sumar: %d", pr.BytesCopied)
	}
}

func TestClassifyCodigosDeSalida(t *testing.T) {
	cases := []struct {
		name    string
		exit    int
		f       Findings
		code    string
		partial bool
	}{
		{"destino inaccesible", exitConnectionDest, Findings{}, codeDestinationFailed, false},
		{"auth destino", exitAuthDest, Findings{}, codeDestinationFailed, false},
		{"tls destino", exitTLS, Findings{DestTLS: true}, codeDestinationFailed, false},
		{"virus rechazado por el destino", exitVirusAppend, Findings{}, codeVirusFound, false},
		{"errores de mensaje", exitWithErrors, Findings{DetectedErrors: 2}, codeImapsyncFailed, true},
		{"errores con cuota", exitWithErrors, Findings{OverQuota: true, DetectedErrors: 1}, codeQuotaExceeded, false},
		{"errores con virus", exitWithErrors, Findings{ScanInfected: 2}, codeVirusFound, true},
		{"antivirus caido al comprobar", exitUsage, Findings{ScanUnavailable: 1}, codeImapsyncFailed, false},
		{"salida desconocida", 77, Findings{}, codeImapsyncFailed, false},
		{"salida 0 con mensajes sin analizar", 0, Findings{ScanUnavailable: 3, NotInDest: 3}, codeImapsyncFailed, false},
		{"salida 0 con mensajes enormes", 0, Findings{ScanTooBig: 1, NotInDest: 1}, codeImapsyncFailed, true},
		{"salida 0 con mensajes sin copiar", 0, Findings{NotInDest: 1}, codeImapsyncFailed, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := classify(c.exit, c.f)
			if v.ok() || v.Err.Code != c.code || v.Partial != c.partial {
				t.Fatalf("veredicto %+v, quiero %s parcial=%v", v, c.code, c.partial)
			}
		})
	}
}

func TestCombineSumaLoCopiadoEntrePasadas(t *testing.T) {
	base := Progress{MessagesCopied: 4, BytesCopied: 100, Folders: []FolderProgress{{Name: "INBOX", MessagesCopied: 4}}}
	cur := Progress{MessagesCopied: 1, BytesCopied: 10, MessagesSkipped: 4, Folders: []FolderProgress{{Name: "INBOX", MessagesCopied: 1, MessagesSkipped: 4}, {Name: "Nueva", MessagesCopied: 0}}}
	got := combine(base, cur)
	if got.MessagesCopied != 5 || got.BytesCopied != 110 || got.MessagesSkipped != 4 {
		t.Fatalf("combinado: %+v", got)
	}
	if got.Folders[0].MessagesCopied != 5 || got.Folders[1].MessagesCopied != 0 {
		t.Fatalf("carpetas combinadas: %+v", got.Folders)
	}
}
