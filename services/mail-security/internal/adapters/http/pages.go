package http

import (
	"bytes"
	"html/template"
	"net/http"
)

// Paginas minimas de los enlaces del aviso de cuarentena. Sin JavaScript (la CSP del
// gateway lo bloquearia) y con estilos en linea, que la politica si permite. El formulario
// hace POST a la misma URL, firma incluida: abrir el enlace (o que lo abra un escaner de
// correo) no libera ni descarta nada; hace falta pulsar el boton. El asunto y el remitente
// son de terceros y los escapa html/template.
var pageTemplate = template.Must(template.New("page").Parse(`<!DOCTYPE html>
<html lang="es">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<meta name="referrer" content="no-referrer">
<title>{{.Title}}</title>
<style>
body{margin:0;padding:0;background:#f4f5f7;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;color:#1f2933}
main{max-width:480px;margin:10vh auto;padding:32px;background:#fff;border-radius:8px;box-shadow:0 1px 4px rgba(0,0,0,.08)}
h1{font-size:20px;margin:0 0 12px}
p{line-height:1.5;margin:0 0 16px}
dl{margin:0 0 16px}
dt{font-size:13px;color:#52606d}
dd{margin:0 0 8px;word-break:break-word}
button{background:#1f2933;color:#fff;border:0;border-radius:6px;padding:12px 20px;font-size:15px;cursor:pointer}
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p>{{.Body}}</p>
{{if .Subject}}<dl><dt>Asunto</dt><dd>{{.Subject}}</dd><dt>Remitente</dt><dd>{{.Sender}}</dd></dl>{{end}}
{{if .Action}}<form method="post" action="{{.Action}}"><button type="submit">{{.Button}}</button></form>{{end}}
</main>
</body>
</html>
`))

type pageData struct {
	Title   string
	Body    string
	Subject string
	Sender  string
	Action  string
	Button  string
}

var (
	// pageInvalidLink es la misma respuesta para una firma alterada, un enlace caducado o
	// usado, un mensaje que ya no esta en cuarentena o una empresa que no existe.
	pageInvalidLink = pageData{Title: "Enlace no válido", Body: "Este enlace no es válido o ya no está vigente. Si necesitas recuperar el mensaje, pídeselo al administrador de tu correo."}
	pageUnavailable = pageData{Title: "Servicio no disponible", Body: "No se pudo completar la operación en este momento. Vuelve a intentarlo en unos minutos."}
	pageReleased    = pageData{Title: "Mensaje liberado", Body: "El mensaje se ha entregado en tu buzón."}
	pageDiscarded   = pageData{Title: "Mensaje descartado", Body: "El mensaje se ha borrado de la cuarentena."}
)

func pageConfirmRelease(action, subject, sender string) pageData {
	return pageData{
		Title:   "Liberar mensaje",
		Body:    "El mensaje se entregará en tu buzón. Libéralo solo si reconoces al remitente: puede ser malicioso.",
		Subject: subject, Sender: sender, Action: action, Button: "Liberar mensaje",
	}
}

func pageConfirmDiscard(action, subject, sender string) pageData {
	return pageData{
		Title:   "Descartar mensaje",
		Body:    "El mensaje se borrará de la cuarentena y no podrá recuperarse.",
		Subject: subject, Sender: sender, Action: action, Button: "Descartar mensaje",
	}
}

func writePage(w http.ResponseWriter, status int, data pageData) {
	var buf bytes.Buffer
	if err := pageTemplate.Execute(&buf, data); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
