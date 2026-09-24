package main

import (
	"fmt"
	"net/http"
	"time"
)

// downloadWriteTimeout es el plazo de escritura de una ruta publica con transfer "download". El del
// servidor (WriteTimeout, 120 s) cortaria la entrega de un fichero de cien megas a una conexion
// lenta sin cuerpo ni rastro. Este es solo un techo: el plazo real lo pone el servicio de destino
// (MAIL_FILES_DOWNLOAD_TIMEOUT, como mucho 6 h), que cancela la respuesta al vencer, y el borde corta
// a un cliente que deja de leer (send_timeout).
const downloadWriteTimeout = 6 * time.Hour

// validatePublicTransfer: transfer solo admite "download" y solo en GET o POST.
func validatePublicTransfer(p publicRouteSpec) error {
	switch p.Transfer {
	case "":
		return nil
	case publicTransferDownload:
		if p.Method != http.MethodGet && p.Method != http.MethodPost {
			return fmt.Errorf("tabla de rutas: transfer %q solo en GET o POST (%s %q)", p.Transfer, p.Method, p.Path)
		}
		return nil
	}
	return fmt.Errorf("tabla de rutas: transfer %q invalido en la ruta publica %s %q (solo %q)", p.Transfer, p.Method, p.Path, publicTransferDownload)
}

// withDownloadDeadline amplia el plazo de escritura de la conexion para esta respuesta. Si el
// ResponseWriter no lo admite se queda el general: la descarga puede cortarse, pero nunca queda
// abierta sin limite.
func withDownloadDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(downloadWriteTimeout))
		next.ServeHTTP(w, r)
	})
}
