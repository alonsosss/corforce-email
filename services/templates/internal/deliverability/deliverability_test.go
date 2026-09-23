package deliverability

import (
	"strings"
	"testing"
)

const (
	testUnsubscribe = "https://example.com/unsubscribe/verificacion"
	testAddress     = "Av. Siempre Viva 742, Lima, Peru"
)

var testBodyText = strings.Repeat("Esta temporada renovamos la coleccion con prendas pensadas para el dia a dia. ", 6)

// mail arma un correo de marketing que no dispara ninguna regla; cada caso cambia una parte.
type mail struct {
	head      string
	preheader string
	body      string
	images    string
	links     string
	footer    string
	subject   string
	address   string
	marketing bool
	spam      Spam
}

func cleanMail() mail {
	return mail{
		head:      `<style>p { color: #111827; }</style>`,
		preheader: `<div style="display:none;font-size:1px;max-height:0px;overflow:hidden;">Novedades de otono para ti</div>`,
		body:      `<h1>Hola</h1><p>` + testBodyText + `</p>`,
		images:    `<img src="https://cdn.acme.pe/portada.png" alt="Coleccion de otono" width="600">`,
		links:     `<a href="https://acme.pe/tienda">Ver la tienda</a>`,
		footer:    `<p>Acme SAC - ` + testAddress + `</p><a href="` + testUnsubscribe + `">Darse de baja</a>`,
		subject:   "Novedades de otono en Acme",
		address:   testAddress,
		marketing: true,
	}
}

func (m mail) analyze() Report {
	html := `<!doctype html><html><head>` + m.head + `</head><body>` + m.preheader +
		`<table width="600" style="max-width:600px;width:100%"><tr><td>` + m.body + m.images + m.links + m.footer +
		`</td></tr></table></body></html>`
	return Analyze(Input{
		Marketing: m.marketing, Subject: m.subject, HTML: html,
		UnsubscribeURL: testUnsubscribe, PhysicalAddress: m.address, Spam: m.spam,
	})
}

func find(r Report, code string) *Issue {
	for i := range r.Issues {
		if r.Issues[i].Code == code {
			return &r.Issues[i]
		}
	}
	return nil
}

func TestUnCorreoLimpioNoTieneIncidencias(t *testing.T) {
	r := cleanMail().analyze()
	if !r.Passed || len(r.Issues) != 0 {
		t.Fatalf("el correo de referencia debe pasar sin incidencias: %+v", r.Issues)
	}
	if r.Stats.Images != 1 || r.Stats.Links != 2 || r.Stats.TextChars < MinTextChars || r.Stats.TextImageRatio < MinTextImageRatio {
		t.Fatalf("estadisticas inesperadas: %+v", r.Stats)
	}
	if r.Spam.Symbols == nil {
		t.Fatal("symbols debe serializarse como lista vacia, no null")
	}
}

type ruleCase struct {
	code     string
	severity string
	fires    func(*mail)
	holds    func(*mail)
}

func TestCadaReglaDisparaYNoDispara(t *testing.T) {
	cases := []ruleCase{
		{CodeMissingUnsubscribe, SeverityError,
			func(m *mail) { m.footer = `<p>Acme SAC - ` + testAddress + `</p><p>` + testUnsubscribe + `</p>` },
			func(m *mail) {
				m.footer = `<p>Acme SAC - ` + testAddress + `</p><a href="` + testUnsubscribe + `&amp;c=1">Baja</a>`
			}},
		{CodeMissingPhysicalAddress, SeverityError,
			func(m *mail) { m.footer = `<a href="` + testUnsubscribe + `">Darse de baja</a>` },
			func(m *mail) {
				m.footer = `<p>AV. SIEMPRE VIVA 742,<br>  Lima, Peru</p><a href="` + testUnsubscribe + `">Baja</a>`
			}},
		{CodeHTMLTooLarge, SeverityError,
			func(m *mail) { m.head += `<style>/*` + strings.Repeat("x", MaxHTMLBytes) + `*/</style>` },
			func(m *mail) { m.head += `<style>/*` + strings.Repeat("x", MaxHTMLBytes/2) + `*/</style>` }},
		{CodeImageOnly, SeverityError,
			func(m *mail) { m.body = `<p>Mira esto</p>` },
			func(m *mail) { m.body = `<p>Mira esto</p>`; m.images = "" }},
		{CodeLinkShortener, SeverityError,
			func(m *mail) {
				m.links = `<a href="https://bit.ly/3abc">Ver</a><a href="https://go.rebrand.ly/x">Mas</a>`
			},
			func(m *mail) { m.links = `<a href="https://bitly.acme.pe/x">Ver</a>` }},
		{CodeDeceptiveLink, SeverityError,
			func(m *mail) { m.links = `<a href="https://acceso-seguro.xyz/login">www.bancopopular.pe</a>` },
			func(m *mail) { m.links = `<a href="https://tienda.acme.pe/x">https://www.acme.pe/tienda</a>` }},
		{CodeForbiddenContent, SeverityError,
			func(m *mail) { m.links = `<a href="javascript:alert(1)">Ver</a><span onclick="x()">y</span>` },
			func(m *mail) { m.links = `<a href="https://acme.pe/on-line">online</a>` }},
		{CodeLowTextRatio, SeverityWarning,
			func(m *mail) { m.images = strings.Repeat(`<img src="https://cdn.acme.pe/p.png" alt="Producto">`, 3) },
			func(m *mail) { m.images = `<img src="https://cdn.acme.pe/p.png" alt="Producto">` }},
		{CodeMissingAlt, SeverityWarning,
			func(m *mail) { m.images = `<img src="https://cdn.acme.pe/p.png">` },
			func(m *mail) { m.images = `<img src="https://cdn.acme.pe/separador.png" alt="">` }},
		{CodeMissingPreheader, SeverityWarning,
			func(m *mail) {
				m.preheader = `<p>Visible primero</p><div style="display:none">Oculto despues</div>`
			},
			func(m *mail) { m.preheader = `<span hidden>Texto de previsualizacion</span>` }},
		{CodeSubjectAllCaps, SeverityWarning,
			func(m *mail) { m.subject = "OFERTA DE OTONO 2026" },
			func(m *mail) { m.subject = "Oferta de IVA 0" }},
		{CodeSubjectPunctuation, SeverityWarning,
			func(m *mail) { m.subject = "Descuentos de otono!!" },
			func(m *mail) { m.subject = "Descuentos de otono!" }},
		{CodeSubjectTooLong, SeverityWarning,
			func(m *mail) { m.subject = strings.Repeat("a", MaxSubjectChars+1) },
			func(m *mail) { m.subject = strings.Repeat("n", MaxSubjectChars) }},
		{CodeSubjectSpamWords, SeverityWarning,
			func(m *mail) { m.subject = "Envio GRATIS y ultima oportunidad" },
			func(m *mail) { m.subject = "Freelancers: la agenda de gratitud" }},
		{CodeInsecureLink, SeverityWarning,
			func(m *mail) { m.links = `<a href="http://acme.pe/tienda">Ver la tienda</a>` },
			func(m *mail) { m.links = `<a href="mailto:hola@acme.pe">hola@acme.pe</a>` }},
		{CodeTooManyLinks, SeverityWarning,
			func(m *mail) { m.links = strings.Repeat(`<a href="https://acme.pe/p">Producto</a>`, MaxLinks) },
			func(m *mail) { m.links = strings.Repeat(`<a href="https://acme.pe/p">Producto</a>`, MaxLinks-2) }},
		{CodeWidthTooLarge, SeverityWarning,
			func(m *mail) { m.body += `<div style="padding:0;width:800px">x</div>` },
			func(m *mail) { m.body += `<div style="max-width:800px;width:100%">x</div><table width="100%"></table>` }},
		{CodeExternalStylesheet, SeverityWarning,
			func(m *mail) { m.head += `<link rel="stylesheet" href="https://cdn.acme.pe/estilos.css">` },
			func(m *mail) {
				m.head += `<link href="https://fonts.googleapis.com/css?family=Inter" rel="stylesheet" type="text/css">` +
					`<style>@import url(https://fonts.googleapis.com/css?family=Inter);</style>`
			}},
		{CodeSpamScoreHigh, SeverityWarning,
			func(m *mail) { m.spam = Spam{Available: true, Score: 6.2, Required: 15, Action: "add header"} },
			func(m *mail) { m.spam = Spam{Available: true, Score: 4.9, Required: 15, Action: "no action"} }},
	}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			fire := cleanMail()
			c.fires(&fire)
			r := fire.analyze()
			issue := find(r, c.code)
			if issue == nil {
				t.Fatalf("la regla no disparo: %+v", r.Issues)
			}
			if issue.Severity != c.severity || issue.Count < 1 || issue.Message == "" {
				t.Fatalf("incidencia mal formada: %+v", issue)
			}
			if r.Passed != (c.severity != SeverityError) {
				t.Fatalf("passed=%v con una incidencia %s", r.Passed, c.severity)
			}
			hold := cleanMail()
			c.holds(&hold)
			if issue := find(hold.analyze(), c.code); issue != nil {
				t.Fatalf("la regla disparo sin motivo: %+v", issue)
			}
		})
	}
}

func TestLasReglasDeMarketingNoAplicanATransaccional(t *testing.T) {
	m := cleanMail()
	m.marketing = false
	m.footer = ""
	m.address = ""
	r := m.analyze()
	if find(r, CodeMissingUnsubscribe) != nil || find(r, CodeMissingPhysicalAddress) != nil {
		t.Fatalf("baja y direccion no aplican a transaccional: %+v", r.Issues)
	}
	m.links = `<a href="https://bit.ly/x">Ver</a>`
	if r := m.analyze(); r.Passed || find(r, CodeLinkShortener) == nil {
		t.Fatalf("el resto de reglas si aplica a transaccional: %+v", r.Issues)
	}
}

func TestSinDireccionEnElKitElMensajeLoDice(t *testing.T) {
	m := cleanMail()
	m.address = ""
	issue := find(m.analyze(), CodeMissingPhysicalAddress)
	if issue == nil || !strings.Contains(issue.Message, "kit de marca") {
		t.Fatalf("sin direccion en el kit: %+v", issue)
	}
}

func TestPuntuacionDeRechazoEsError(t *testing.T) {
	for _, s := range []Spam{
		{Available: true, Score: 16, Required: 15, Action: "reject"},
		{Available: true, Score: 3, Required: 15, Action: ActionReject},
		{Available: true, Score: 15, Required: 15, Action: "add header"},
	} {
		m := cleanMail()
		m.spam = s
		r := m.analyze()
		issue := find(r, CodeSpamScoreHigh)
		if issue == nil || issue.Severity != SeverityError || r.Passed {
			t.Fatalf("%+v debe ser error: %+v", s, issue)
		}
	}
	m := cleanMail()
	m.spam = Spam{Score: 40, Action: ActionReject}
	if find(m.analyze(), CodeSpamScoreHigh) != nil {
		t.Fatal("sin puntuacion disponible no hay incidencia de spam")
	}
}

func TestLosErroresVanAntesQueLosAvisos(t *testing.T) {
	m := cleanMail()
	m.subject = "GRATIS!!"
	m.links = `<a href="https://bit.ly/x">Ver</a>`
	r := m.analyze()
	seenWarning := false
	for _, i := range r.Issues {
		if i.Severity == SeverityWarning {
			seenWarning = true
		} else if seenWarning {
			t.Fatalf("un error despues de un aviso: %+v", r.Issues)
		}
	}
	if len(r.Errors()) != 1 {
		t.Fatalf("errores: %+v", r.Errors())
	}
}

func TestLasEstadisticasCuentanTextoVisible(t *testing.T) {
	r := Analyze(Input{Subject: "Hola", HTML: `<html><head><title>No cuenta</title><style>p{}</style></head>` +
		`<body><div style="display:none">oculto</div><p>Hola   mundo</p><img src="https://a.pe/x.png" alt="x"></body></html>`})
	if r.Stats.TextChars != len("Hola mundo") {
		t.Fatalf("texto visible: %d", r.Stats.TextChars)
	}
	if r.Stats.TextImageRatio != 0.05 {
		t.Fatalf("proporcion: %v", r.Stats.TextImageRatio)
	}
	if r := Analyze(Input{Subject: "Hola", HTML: `<p>Hola</p>`}); r.Stats.TextImageRatio != 1 {
		t.Fatalf("sin imagenes la proporcion es 1: %v", r.Stats.TextImageRatio)
	}
}
