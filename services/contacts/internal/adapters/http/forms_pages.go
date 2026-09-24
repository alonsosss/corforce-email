package http

import (
	"html/template"
	"io"
)

type pageRenderer interface {
	Execute(w io.Writer, data any) error
}

type embedField struct {
	Name         string
	Label        string
	Required     bool
	Placeholder  string
	Input        string
	Autocomplete string
	Value        string
	Checked      bool
}

type embedPage struct {
	Title         string
	Description   string
	SubmitLabel   string
	ConsentText   string
	Action        string
	Token         string
	TopTarget     bool
	Error         string
	Fields        []embedField
	TokenField    string
	ConsentField  string
	HoneypotField string
}

type messagePage struct {
	Title string
	Body  string
}

// formStyles son los estilos comunes de las paginas del formulario: en linea (la CSP no admite
// hojas externas) y sobrios, para encajar en el sitio que lo incrusta.
const formStyles = `
body{margin:0;padding:16px;background:transparent;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;color:#1f2933;font-size:15px}
main{max-width:520px;margin:0 auto}
h1{font-size:20px;margin:0 0 8px}
p{line-height:1.5;margin:0 0 12px;white-space:pre-line}
label{display:block;margin:0 0 4px;font-weight:600}
.field{margin:0 0 14px}
input[type=text],input[type=email],input[type=number],input[type=date]{box-sizing:border-box;width:100%;padding:10px 12px;border:1px solid #cbd2d9;border-radius:6px;font-size:15px}
.check{display:flex;gap:8px;align-items:flex-start;font-weight:400}
.check span{white-space:pre-line;line-height:1.4}
.error{border:1px solid #d64545;background:#fdecec;color:#8a1c1c;border-radius:6px;padding:10px 12px;margin:0 0 14px}
.hp{position:absolute;left:-10000px;top:auto;width:1px;height:1px;overflow:hidden}
button{background:#1f2933;color:#fff;border:0;border-radius:6px;padding:12px 20px;font-size:15px;cursor:pointer}
`

// embedTemplate es el formulario del iframe. Sin JavaScript: envia por POST, el token firmado
// va oculto y el campo trampa queda fuera de la vista y del tabulador.
var embedTemplate = template.Must(template.New("embed").Parse(`<!DOCTYPE html>
<html lang="es">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<meta name="referrer" content="no-referrer">
<title>{{if .Title}}{{.Title}}{{else}}Suscripción{{end}}</title>
<style>` + formStyles + `</style>
</head>
<body>
<main>
{{if .Title}}<h1>{{.Title}}</h1>{{end}}
{{if .Description}}<p>{{.Description}}</p>{{end}}
{{if .Error}}<div class="error" role="alert">{{.Error}}</div>{{end}}
<form method="post" action="{{.Action}}"{{if .TopTarget}} target="_top"{{end}} accept-charset="utf-8">
<input type="hidden" name="{{.TokenField}}" value="{{.Token}}">
{{range .Fields}}{{if eq .Input "checkbox"}}<div class="field"><label class="check"><input type="checkbox" name="{{.Name}}" value="on"{{if .Checked}} checked{{end}}><span>{{.Label}}</span></label></div>
{{else}}<div class="field"><label for="{{.Name}}">{{.Label}}{{if .Required}} *{{end}}</label><input id="{{.Name}}" type="{{.Input}}" name="{{.Name}}" value="{{.Value}}"{{if .Placeholder}} placeholder="{{.Placeholder}}"{{end}} autocomplete="{{.Autocomplete}}"{{if .Required}} required{{end}}{{if eq .Input "text"}} maxlength="1000"{{end}}{{if eq .Input "number"}} step="any"{{end}}></div>
{{end}}{{end}}<div class="hp" aria-hidden="true"><label for="{{.HoneypotField}}">Deja este campo vacío</label><input id="{{.HoneypotField}}" type="text" name="{{.HoneypotField}}" value="" tabindex="-1" autocomplete="off"></div>
<div class="field"><label class="check"><input type="checkbox" name="{{.ConsentField}}" value="on" required><span>{{.ConsentText}}</span></label></div>
<button type="submit">{{.SubmitLabel}}</button>
</form>
</main>
</body>
</html>
`))

// messageTemplate es la pagina de gracias o de un fallo que no se corrige en el formulario.
var messageTemplate = template.Must(template.New("message").Parse(`<!DOCTYPE html>
<html lang="es">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<meta name="referrer" content="no-referrer">
<title>{{.Title}}</title>
<style>` + formStyles + `</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p>{{.Body}}</p>
</main>
</body>
</html>
`))
