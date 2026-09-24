package http

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"

	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// PublicPattern es la ruta del enlace, la que declara el gateway en routes.json.
const PublicPattern = domain.DownloadPath + "/{tenant}/{file}"

// downloadCSP es la politica de la respuesta con el fichero: nada se ejecuta ni se incrusta aunque un
// navegador intentara mostrarlo.
const downloadCSP = "default-src 'none'; frame-ancestors 'none'; sandbox"

// Pagina minima del enlace. Sin JavaScript (la CSP del gateway lo bloquearia) y con estilos en linea,
// que la politica si permite. Abrir el enlace, o que lo abra el analizador de enlaces de un correo,
// no gasta ninguna descarga: el boton hace POST a la misma URL, firma incluida. El nombre del fichero
// lo eligio el remitente y lo escapa html/template.
var pageTemplate = template.Must(template.New("page").Parse(`<!DOCTYPE html>
<html lang="es">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<meta name="referrer" content="no-referrer">
<title>{{.Title}}</title>
<style>
body{margin:0;padding:0;background:#f4f5f7;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;color:#1f2933}
main{max-width:480px;margin:10vh auto;padding:32px;background:#fff;border-radius:8px;box-shadow:0 1px 4px rgba(0,0,0,.08)}
h1{font-size:20px;margin:0 0 12px}
p{line-height:1.5;margin:0 0 16px}
dl{margin:0 0 16px}
dt{font-size:13px;color:#52606d}
dd{margin:0 0 8px;word-break:break-all}
code{font-size:12px}
button{background:#1f2933;color:#fff;border:0;border-radius:6px;padding:12px 20px;font-size:15px;cursor:pointer}
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p>{{.Body}}</p>
{{if .File}}<dl>
<dt>Fichero</dt><dd>{{.File.Name}}</dd>
<dt>Tamano</dt><dd>{{.File.Size}}</dd>
<dt>Disponible hasta</dt><dd>{{.File.Expires}}</dd>
<dt>Descargas restantes</dt><dd>{{.File.Remaining}}</dd>
<dt>Huella SHA-256</dt><dd><code>{{.File.SHA256}}</code></dd>
</dl>
<form method="post" action="{{.Action}}"><button type="submit">Descargar</button></form>{{end}}
</main>
</body>
</html>
`))

type pageFile struct {
	Name      string
	Size      string
	Expires   string
	Remaining int
	SHA256    string
}

type pageData struct {
	Title  string
	Body   string
	File   *pageFile
	Action string
}

var (
	// pageInvalidLink es la misma respuesta para cualquier enlace que no sirve: firma alterada,
	// caducado, revocado, agotado o de una empresa que no existe.
	pageInvalidLink = pageData{Title: "Enlace no valido", Body: "Este enlace no es valido o ya no esta vigente. Si necesitas el fichero, pideselo de nuevo a quien te lo envio."}
	pageUnavailable = pageData{Title: "Servicio no disponible", Body: "No se pudo completar la operacion en este momento. Vuelve a intentarlo en unos minutos."}
)

func pageDownload(f domain.File, action string) pageData {
	return pageData{
		Title: "Fichero compartido",
		Body:  "Te han enviado este fichero por enlace. Se analizo con antivirus antes de publicarse.",
		File: &pageFile{
			Name: f.Name, Size: humanSize(f.SizeBytes), Expires: f.ExpiresAt.UTC().Format("02/01/2006 15:04") + " UTC",
			Remaining: f.RemainingDownloads(), SHA256: f.SHA256,
		},
		Action: action,
	}
}

func writePage(w http.ResponseWriter, status int, data pageData) {
	var buf bytes.Buffer
	if err := pageTemplate.Execute(&buf, data); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// humanSize escribe un tamano en unidades binarias con coma decimal.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	value := strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64)
	return fmt.Sprintf("%s %ciB", commaDecimal(value), "KMGTPE"[exp])
}

func commaDecimal(s string) string {
	return string(bytes.Replace([]byte(s), []byte("."), []byte(","), 1))
}

func (h *Handler) claims(r *http.Request) (domain.LinkClaims, string, error) {
	q := r.URL.Query()
	return domain.ParseLink(chi.URLParam(r, "tenant"), chi.URLParam(r, "file"), q.Get("x"), q.Get("s"))
}

func (h *Handler) publicFailure(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrLinkInvalid) {
		writePage(w, http.StatusNotFound, pageInvalidLink)
		return
	}
	h.logger.Warn("mail-files: enlace publico no servido", zap.String("path", r.URL.Path), zap.Error(err))
	writePage(w, http.StatusServiceUnavailable, pageUnavailable)
}

// Landing es lo que abre el enlace: el fichero y el boton de descarga, sin contar descarga.
func (h *Handler) Landing(w http.ResponseWriter, r *http.Request) {
	c, sig, err := h.claims(r)
	if err != nil {
		h.publicFailure(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.OperationTimeout)
	defer cancel()
	file, err := h.uc.Inspect(ctx, c, sig)
	if err != nil {
		h.publicFailure(w, r, err)
		return
	}
	writePage(w, http.StatusOK, pageDownload(file, r.URL.RequestURI()))
}

// Serve entrega el fichero y cuenta la descarga. Siempre como adjunto y como octet-stream, con el
// nombre saneado: el navegador lo guarda y nunca lo interpreta.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request) {
	c, sig, err := h.claims(r)
	if err != nil {
		h.publicFailure(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.DownloadTimeout)
	defer cancel()
	file, content, err := h.uc.Download(ctx, c, sig)
	if err != nil {
		h.publicFailure(w, r, err)
		return
	}
	defer content.Close()
	extendDeadlines(w, h.cfg.DownloadTimeout)
	writeDownloadHeaders(w.Header(), file)
	w.WriteHeader(http.StatusOK)
	if n, err := io.Copy(w, content); err != nil {
		h.logger.Info("mail-files: descarga interrumpida", zap.String("file_id", file.ID.String()),
			zap.Int64("sent", n), zap.Int64("size", file.SizeBytes), zap.Error(err))
	}
}

func writeDownloadHeaders(h http.Header, f domain.File) {
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", ContentDisposition(f.Name))
	h.Set("Content-Length", strconv.FormatInt(f.SizeBytes, 10))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Download-Options", "noopen")
	h.Set("Content-Security-Policy", downloadCSP)
	h.Set("Cache-Control", "no-store")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	if sum, err := hex.DecodeString(f.SHA256); err == nil {
		h.Set("Repr-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(sum)+":")
	}
}

// ContentDisposition es la cabecera de un adjunto con el nombre en ASCII (filename) y en UTF-8
// (filename*, RFC 6266 y 5987).
func ContentDisposition(name string) string {
	return `attachment; filename="` + domain.ASCIIFileName(name) + `"; filename*=UTF-8''` + domain.RFC5987(name)
}
