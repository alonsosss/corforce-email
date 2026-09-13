package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"strconv"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

const (
	// pendingMargin alarga la reserva de una clave mas alla del plazo del envio: si
	// caducara con el envio aun en marcha, un reintento entraria y lo entregaria otra vez.
	pendingMargin = time.Minute
	// ledgerWriteTimeout acota cada escritura del registro que se hace sin la cancelacion de
	// la peticion: una vez que Postfix acepto el mensaje, su marca no puede perderse porque
	// el navegador cierre la conexion.
	ledgerWriteTimeout = 5 * time.Second
)

// sendKey es la clave del registro de envios: el hash del buzon y de la clave del cliente.
// La clave de un buzon no puede leer ni pisar el registro de otro.
func sendKey(username, idempotencyKey string) string {
	sum := sha256.Sum256([]byte(username + "\x00" + idempotencyKey))
	return hex.EncodeToString(sum[:])
}

// fingerprint resume el mensaje tal como lo pidio el cliente, antes de sanearlo o de traer
// adjuntos del buzon: el reintento de un envio ya hecho se reconoce sin volver a leer nada
// (el borrador de origen pudo retirarse con el primer intento).
func fingerprint(d domain.Draft, replaceUID uint32) string {
	h := sha256.New()
	field := func(v string) { writeField(h, v) }
	address := func(a domain.Address) {
		field(strings.ToLower(a.Email))
		field(a.Name)
	}
	address(d.From)
	for _, list := range [][]domain.Address{d.To, d.Cc, d.Bcc} {
		field(strconv.Itoa(len(list)))
		for _, a := range list {
			address(a)
		}
	}
	field(d.Subject)
	field(d.Text)
	field(d.HTML)
	field(strconv.Itoa(len(d.Attachments)))
	for _, a := range d.Attachments {
		sum := sha256.Sum256(a.Data)
		field(a.Filename)
		field(a.ContentType)
		field(hex.EncodeToString(sum[:]))
	}
	if t := d.InReplyTo; t != nil {
		field("reply")
		field(t.Folder)
		field(strconv.FormatUint(uint64(t.UID), 10))
	}
	if src := d.Source; src != nil {
		field("source")
		field(src.Folder)
		field(strconv.FormatUint(uint64(src.UID), 10))
		field(strings.Join(src.Parts, ","))
	}
	field(strconv.FormatUint(uint64(replaceUID), 10))
	return hex.EncodeToString(h.Sum(nil))
}

// writeField escribe el campo con su longitud delante: ningun par de valores distintos
// produce la misma secuencia.
func writeField(h hash.Hash, v string) {
	fmt.Fprintf(h, "%d:%s;", len(v), v)
}

// record guarda el estado del envio con la vida de una sesion: un reintento solo puede
// llegar desde una sesion abierta. Un fallo se registra; si la marca de enviado no quedo,
// un reintento tras caducar la reserva volveria a entregar el mensaje, por eso es un error.
func (s *Service) record(ctx context.Context, key string, rec domain.SendRecord) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ledgerWriteTimeout)
	defer cancel()
	ok, err := s.ledger.Update(ctx, key, rec, s.cfg.Sessions.Max)
	switch {
	case err != nil:
		s.logger.Error("webmail: no se pudo guardar el estado del envio", zap.String("state", string(rec.State)), zap.Error(err))
	case !ok:
		s.logger.Error("webmail: la reserva del envio caduco antes de guardar su estado", zap.String("state", string(rec.State)))
	}
}

// release libera la clave de un envio que no salio.
func (s *Service) release(ctx context.Context, key, token string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ledgerWriteTimeout)
	defer cancel()
	if err := s.ledger.Release(ctx, key, token); err != nil {
		// La reserva caduca sola: hasta entonces un reintento recibe ErrSendInProgress.
		s.logger.Warn("webmail: no se pudo liberar la clave del envio", zap.Error(err))
	}
}
