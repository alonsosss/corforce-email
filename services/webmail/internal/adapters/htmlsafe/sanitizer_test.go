package htmlsafe

import (
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func incoming(html string, allowRemote bool) domain.SanitizedHTML {
	return New().Incoming(html, domain.SanitizeOptions{
		AllowRemoteImages: allowRemote,
		ResolveCID: func(cid string) (string, bool) {
			if cid == "img1@x" {
				return "/api/v1/webmail/folders/INBOX/messages/9/parts/2", true
			}
			return "", false
		},
	})
}

func assertAbsent(t *testing.T, got string, forbidden ...string) {
	t.Helper()
	lower := strings.ToLower(got)
	for _, f := range forbidden {
		if strings.Contains(lower, strings.ToLower(f)) {
			t.Errorf("la salida contiene %q: %s", f, got)
		}
	}
}

func TestQuitaScriptsYManejadores(t *testing.T) {
	out := incoming(`<script>alert(1)</script><p onclick="robar()">hola</p>`+
		`<img src="https://x.test/a.png" onerror="alert(2)"><svg><script>alert(3)</script></svg>`+
		`<body onload="x()"><div onmouseover="y()">z</div>`, true).HTML
	assertAbsent(t, out, "<script", "alert", "onclick", "onerror", "onload", "onmouseover", "<svg")
	if !strings.Contains(out, "<p>hola</p>") {
		t.Fatalf("el texto se conserva: %s", out)
	}
}

func TestQuitaEsquemasPeligrosos(t *testing.T) {
	for _, href := range []string{
		"javascript:alert(1)", "JaVaScRiPt:alert(1)", "  javascript:alert(1)",
		"&#106;avascript:alert(1)", "java&#x09;script:alert(1)", "vbscript:msgbox(1)",
		"data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==", "cid:img1@x", "file:///etc/passwd",
	} {
		out := incoming(`<a href="`+href+`">x</a>`, true).HTML
		assertAbsent(t, out, "javascript", "vbscript", "data:", "cid:", "file:", "href")
	}
}

func TestEnlacesSeguros(t *testing.T) {
	out := incoming(`<a href="https://banco.test/login">entra</a> <a href="mailto:ana@x.com">ana</a>`, false).HTML
	for _, want := range []string{`href="https://banco.test/login"`, "noopener", "noreferrer", "nofollow", `target="_blank"`, `href="mailto:ana@x.com"`} {
		if !strings.Contains(out, want) {
			t.Errorf("falta %q: %s", want, out)
		}
	}
}

func TestEstilosQueCarganRecursos(t *testing.T) {
	out := incoming(`<p style="background:url(https://t.test/x.png); color: red">a</p>`+
		`<div style="background-image:url(https://t.test/y.png)">b</div>`+
		`<p style="width: expression(alert(1))">c</p>`+
		`<p style="border: 1px solid url(https://t.test/z.png)">d</p>`+
		`<ul style="list-style: url(https://t.test/l.png)"><li>e</li></ul>`+
		`<span style="font-family: url(https://t.test/f.woff)">f</span>`+
		`<style>body{background:url(https://t.test/s.png)}</style>`+
		`<link rel="stylesheet" href="https://t.test/a.css">`, true).HTML
	assertAbsent(t, out, "url(", "expression", "t.test", "<style", "<link")
	if !strings.Contains(out, "color: red") {
		t.Fatalf("un estilo inofensivo se conserva: %s", out)
	}
}

func TestQuitaFormulariosMarcosYMeta(t *testing.T) {
	out := incoming(`<form action="https://x.test/robar"><input name="pass"><button>Enviar</button></form>`+
		`<iframe src="https://x.test"></iframe><object data="https://x.test/o"></object><embed src="https://x.test/e">`+
		`<meta http-equiv="refresh" content="0;url=https://x.test"><base href="https://x.test/">`+
		`<a id="clobber" name="clobber">n</a><table background="https://x.test/bg.png"><tr><td>t</td></tr></table>`+
		`<img src="cid:img1@x" srcset="https://x.test/2x.png 2x">`, true).HTML
	assertAbsent(t, out, "<form", "<input", "<button", "<iframe", "<object", "<embed", "<meta", "<base",
		"x.test", `id="clobber"`, `name="clobber"`, "background=", "srcset")
}

func TestImagenesRemotasBloqueadasPorDefecto(t *testing.T) {
	raw := `<p>hola</p><img src="https://tracker.test/pixel.gif" alt="logo">`
	blocked := incoming(raw, false)
	if !blocked.RemoteImages || strings.Contains(blocked.HTML, "tracker.test") {
		t.Fatalf("bloqueada: %+v", blocked)
	}
	if !strings.Contains(blocked.HTML, `alt="logo"`) {
		t.Fatalf("la imagen se sustituye, no desaparece: %s", blocked.HTML)
	}
	allowed := incoming(raw, true)
	if !allowed.RemoteImages || !strings.Contains(allowed.HTML, `src="https://tracker.test/pixel.gif"`) {
		t.Fatalf("permitida: %+v", allowed)
	}
	if incoming(`<p>sin imagenes</p>`, false).RemoteImages {
		t.Fatal("sin imagenes remotas no hay aviso")
	}
}

func TestImagenesCID(t *testing.T) {
	out := incoming(`<img src="cid:img1@x"><img src="cid:img1%40x"><img src="cid:otra@x" alt="o">`, false)
	if strings.Count(out.HTML, `src="/api/v1/webmail/folders/INBOX/messages/9/parts/2"`) != 2 {
		t.Fatalf("cid resuelto y cid con escapes (RFC 2392): %s", out.HTML)
	}
	if strings.Contains(out.HTML, "otra@x") || strings.Contains(out.HTML, "cid:") {
		t.Fatalf("un cid desconocido pierde el src: %s", out.HTML)
	}
	if out.RemoteImages {
		t.Fatal("una imagen cid no es remota")
	}
}

func TestImagenesData(t *testing.T) {
	png := `data:image/png;base64,iVBORw0KGgo=`
	out := incoming(`<img src="`+png+`"><img src="data:image/svg+xml;base64,PHN2Zz48L3N2Zz4="><img src="data:text/html;base64,PGI+">`, false).HTML
	if !strings.Contains(out, png) {
		t.Fatalf("una imagen png en linea se conserva: %s", out)
	}
	assertAbsent(t, out, "svg+xml", "text/html")
}

func TestHTMLVacio(t *testing.T) {
	if got := incoming("   ", false); got.HTML != "" || got.RemoteImages {
		t.Fatalf("got %+v", got)
	}
}

func TestSalienteConservaImagenesYQuitaScripts(t *testing.T) {
	clean, plain := New().Outgoing(`<p>Hola <b>Luis</b></p><script>alert(1)</script><img src="https://x.test/logo.png"><img src="cid:firma@x"><div>Saludos<br>Ana</div>`)
	assertAbsent(t, clean, "<script", "alert")
	for _, want := range []string{"https://x.test/logo.png", "cid:firma@x"} {
		if !strings.Contains(clean, want) {
			t.Errorf("falta %q: %s", want, clean)
		}
	}
	if plain != "Hola Luis\n\nSaludos\nAna" {
		t.Fatalf("texto plano: %q", plain)
	}
}
