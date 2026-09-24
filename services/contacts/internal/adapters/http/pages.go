package http

import (
	"bytes"
	"html/template"
	"net/http"
)

// Paginas minimas del doble opt-in. Sin JavaScript (la CSP del gateway lo bloquearia) y
// con estilos en linea, que la politica si permite. El formulario hace POST a la misma
// URL, token incluido: abrir el enlace (o que lo abra un escaner de correo) no confirma
// nada; confirmar exige pulsar el boton.
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
button{background:#1f2933;color:#fff;border:0;border-radius:6px;padding:12px 20px;font-size:15px;cursor:pointer}
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p>{{.Body}}</p>
{{if .Action}}<form method="post" action="{{.Action}}"><button type="submit">Confirmar suscripción</button></form>{{end}}
</main>
</body>
</html>
`))

type pageData struct {
	Title  string
	Body   string
	Action string
}

var (
	// pageInvalidLink es la misma respuesta para un token inexistente, usado, caducado o
	// de otra empresa: quien prueba enlaces no aprende nada de cual fallo.
	pageInvalidLink = pageData{Title: "Enlace no válido", Body: "Este enlace de confirmación no es válido o ya no está vigente. Si quieres suscribirte, vuelve a solicitarlo al remitente."}
	pageUnavailable = pageData{Title: "Servicio no disponible", Body: "No se pudo registrar la confirmación en este momento. Vuelve a intentarlo en unos minutos."}
	pageConfirmed   = pageData{Title: "Suscripción confirmada", Body: "Gracias. Tu suscripción ha quedado confirmada."}
)

func pageConfirm(action string) pageData {
	return pageData{
		Title:  "Confirmar suscripción",
		Body:   "Pulsa el botón para confirmar que quieres recibir comunicaciones de este remitente.",
		Action: action,
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
