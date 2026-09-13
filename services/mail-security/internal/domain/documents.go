package domain

import "time"

// DocumentRspamdSettings es el documento /settings que Rspamd sondea con If-Modified-Since.
const DocumentRspamdSettings = "rspamd_settings"

// DocumentStamp es la marca de modificacion de un documento de los motores, compartida por
// todas las replicas: el hash del contenido y desde cuando es ese contenido.
type DocumentStamp struct {
	Document     string
	ContentHash  string
	LastModified time.Time
}

// NextStamp decide la marca de un documento cuyo contenido tiene el hash dado, a partir de
// la guardada (nil si no hay ninguna). changed indica que hay que guardarla.
//
// Las fechas HTTP tienen resolucion de segundo: la marca nueva siempre supera a la
// anterior en al menos un segundo, o un cambio en el mismo segundo se perderia. seed es el
// ultimo updated_at de las politicas y solo cuenta para el primer documento: si nada
// cambio desde entonces, Rspamd no necesita recargar lo que ya tiene.
func NextStamp(prev *DocumentStamp, document, hash string, now, seed time.Time) (next DocumentStamp, changed bool) {
	if prev != nil && prev.ContentHash == hash {
		return *prev, false
	}
	since := now.UTC().Truncate(time.Second)
	if prev == nil && !seed.IsZero() && seed.Before(since) {
		since = seed.UTC().Truncate(time.Second)
	}
	if prev != nil && !since.After(prev.LastModified) {
		since = prev.LastModified.Add(time.Second)
	}
	return DocumentStamp{Document: document, ContentHash: hash, LastModified: since}, true
}
