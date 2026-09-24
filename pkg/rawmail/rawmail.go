// Package rawmail lee y limpia un mensaje MIME crudo que llega de fuera (el DATA de una sesion
// SMTP de smtp-relay) antes de que salga por Amazon SES. Lo usan los dos lados: smtp-relay para
// rechazar en la sesion lo malformado y saber si lleva adjuntos que analizar, y transactional
// para leer el remitente, el asunto y los cuerpos con los que aplica sus reglas. Nunca confia en
// lo que dice el mensaje: cada limite se comprueba aqui.
package rawmail

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	gomessage "github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // juegos de caracteres de los cuerpos (latin1, windows-1252...)
	"github.com/emersion/go-message/mail"
)

// Limites de SES v2 para un mensaje crudo y de RFC 5322.
const (
	// MaxSESBytes es el tamano maximo de un mensaje de SES v2, adjuntos incluidos.
	MaxSESBytes = 40 << 20
	// MaxSESParts es el numero maximo de partes MIME que admite SES.
	MaxSESParts = 500
	// MaxLineOctets es el largo maximo de una linea sin el CRLF (RFC 5322, 2.1.1).
	MaxLineOctets = 998
	// DefaultMaxDepth acota el anidamiento de multipartes: un mensaje legitimo rara vez pasa de
	// cuatro niveles y la recursion no debe quedar en manos de quien lo escribe.
	DefaultMaxDepth = 10
	// maxBodyText acota lo que se guarda de cada cuerpo de texto para mostrarlo.
	maxBodyText = 10 << 20
)

var (
	ErrMalformed    = errors.New("rawmail: mensaje MIME malformado")
	ErrTooLarge     = errors.New("rawmail: el mensaje supera el tamano maximo")
	ErrTooManyParts = errors.New("rawmail: el mensaje supera el numero de partes MIME")
	ErrLineTooLong  = errors.New("rawmail: una linea supera 998 octetos")
	ErrFrom         = errors.New("rawmail: el mensaje debe llevar exactamente un remitente en From")
)

// Limits son los topes del analisis; los ceros toman los de SES.
type Limits struct {
	MaxBytes int
	MaxParts int
	MaxDepth int
}

func (l Limits) withDefaults() Limits {
	if l.MaxBytes <= 0 || l.MaxBytes > MaxSESBytes {
		l.MaxBytes = MaxSESBytes
	}
	if l.MaxParts <= 0 || l.MaxParts > MaxSESParts {
		l.MaxParts = MaxSESParts
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	return l
}

// Address es una direccion tal como la trae la cabecera, con su nombre visible; normalizarla es
// cosa de quien aplica sus reglas.
type Address struct {
	Name  string
	Email string
}

// Message es lo que se sabe del mensaje tras leerlo.
type Message struct {
	From    Address
	Sender  *Address
	ReplyTo []string
	Subject string
	// MessageID es la cabecera Message-ID sin los angulos, vacia si no la trae.
	MessageID string
	Text      string
	HTML      string
	// HasAttachments: alguna parte no es un cuerpo de texto (adjunto, imagen en linea, mensaje
	// reenviado). Es lo que exige pasar por ClamAV.
	HasAttachments bool
	Parts          int
}

// Parse lee el mensaje con los limites dados. Un error envuelve uno de los Err* del paquete.
func Parse(raw []byte, lim Limits) (*Message, error) {
	lim = lim.withDefaults()
	if len(raw) > lim.MaxBytes {
		return nil, ErrTooLarge
	}
	if err := checkLines(raw); err != nil {
		return nil, err
	}
	entity, err := gomessage.Read(bytes.NewReader(raw))
	if err != nil && !gomessage.IsUnknownCharset(err) && !gomessage.IsUnknownEncoding(err) {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	header := mail.Header{Header: entity.Header}
	msg := &Message{}
	if err := readAddresses(header, msg); err != nil {
		return nil, err
	}
	if msg.Subject, err = header.Subject(); err != nil {
		msg.Subject = header.Get("Subject")
	}
	if id, err := header.MessageID(); err == nil {
		msg.MessageID = id
	}
	w := walker{lim: lim, msg: msg}
	if err := w.walk(entity, 0); err != nil {
		return nil, err
	}
	return msg, nil
}

func readAddresses(h mail.Header, msg *Message) error {
	from, err := h.AddressList("From")
	if err != nil || len(from) != 1 || from[0].Address == "" {
		return ErrFrom
	}
	msg.From = Address{Name: strings.TrimSpace(from[0].Name), Email: from[0].Address}
	if h.Has("Sender") {
		sender, err := h.AddressList("Sender")
		if err != nil || len(sender) != 1 {
			return fmt.Errorf("%w: Sender ilegible", ErrMalformed)
		}
		msg.Sender = &Address{Name: sender[0].Name, Email: sender[0].Address}
	}
	if h.Has("Reply-To") {
		reply, err := h.AddressList("Reply-To")
		if err != nil {
			return fmt.Errorf("%w: Reply-To ilegible", ErrMalformed)
		}
		for _, a := range reply {
			msg.ReplyTo = append(msg.ReplyTo, a.Address)
		}
	}
	return nil
}

type walker struct {
	lim Limits
	msg *Message
}

func (w *walker) walk(e *gomessage.Entity, depth int) error {
	w.msg.Parts++
	if w.msg.Parts > w.lim.MaxParts {
		return ErrTooManyParts
	}
	if depth > w.lim.MaxDepth {
		return fmt.Errorf("%w: anidamiento de partes excesivo", ErrMalformed)
	}
	if mr := e.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil && !gomessage.IsUnknownCharset(err) && !gomessage.IsUnknownEncoding(err) {
				return fmt.Errorf("%w: %v", ErrMalformed, err)
			}
			if part == nil {
				return fmt.Errorf("%w: parte ilegible", ErrMalformed)
			}
			if err := w.walk(part, depth+1); err != nil {
				return err
			}
		}
	}
	mediaType, _, err := e.Header.ContentType()
	if err != nil {
		mediaType = "text/plain"
	}
	disposition, _, _ := e.Header.ContentDisposition()
	body := func() (string, error) {
		b, err := io.ReadAll(io.LimitReader(e.Body, maxBodyText+1))
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		if len(b) > maxBodyText {
			b = b[:maxBodyText]
		}
		return string(b), nil
	}
	switch {
	case strings.EqualFold(disposition, "attachment"):
		w.msg.HasAttachments = true
	case mediaType == "text/plain" && w.msg.Text == "":
		text, err := body()
		if err != nil {
			return err
		}
		w.msg.Text = text
	case mediaType == "text/html" && w.msg.HTML == "":
		html, err := body()
		if err != nil {
			return err
		}
		w.msg.HTML = html
	case strings.HasPrefix(mediaType, "text/"):
	default:
		w.msg.HasAttachments = true
	}
	// Se drena lo que no se leyo: un error de decodificacion al final de la parte tambien es un
	// mensaje malformado.
	if _, err := io.Copy(io.Discard, e.Body); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return nil
}

func checkLines(raw []byte) error {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 4096), MaxLineOctets+2)
	for sc.Scan() {
		if len(bytes.TrimSuffix(sc.Bytes(), []byte("\r"))) > MaxLineOctets {
			return ErrLineTooLong
		}
	}
	if errors.Is(sc.Err(), bufio.ErrTooLong) {
		return ErrLineTooLong
	}
	return sc.Err()
}

// droppedHeader dice que cabeceras no pueden salir tal como llegan: Bcc revelaria a los
// destinatarios ocultos, Return-Path lo fija quien entrega, y las X-SES-* son instrucciones a
// SES (conjunto de configuracion, etiquetas, identidad de otra cuenta con la que firmar) que una
// empresa no puede dar por su cuenta: cambiarian el carril, la atribucion de los eventos o la
// identidad de envio.
func droppedHeader(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "bcc" || n == "return-path" || strings.HasPrefix(n, "x-ses-")
}

// Sanitize devuelve el mensaje con los finales de linea en CRLF, sin las cabeceras que no pueden
// salir (droppedHeader) y con Date y MIME-Version si faltaban. El cuerpo no se toca.
func Sanitize(raw []byte, now time.Time) ([]byte, error) {
	normalized := toCRLF(raw)
	headerEnd := bytes.Index(normalized, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		return nil, fmt.Errorf("%w: sin separación entre cabecera y cuerpo", ErrMalformed)
	}
	head, body := normalized[:headerEnd+2], normalized[headerEnd+2:]

	var out bytes.Buffer
	out.Grow(len(normalized) + 64)
	hasDate, hasMIME := false, false
	keep := true
	for _, line := range bytes.SplitAfter(head, []byte("\r\n")) {
		if len(line) == 0 {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if keep {
				out.Write(line)
			}
			continue
		}
		name, _, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			return nil, fmt.Errorf("%w: línea de cabecera sin nombre", ErrMalformed)
		}
		keep = !droppedHeader(string(name))
		switch strings.ToLower(strings.TrimSpace(string(name))) {
		case "date":
			hasDate = true
		case "mime-version":
			hasMIME = true
		}
		if keep {
			out.Write(line)
		}
	}
	if !hasDate {
		out.WriteString("Date: " + now.UTC().Format(time.RFC1123Z) + "\r\n")
	}
	if !hasMIME {
		out.WriteString("MIME-Version: 1.0\r\n")
	}
	out.Write(body)
	return out.Bytes(), nil
}

// toCRLF convierte los LF sueltos en CRLF sin duplicar los que ya lo son.
func toCRLF(raw []byte) []byte {
	if !bytes.Contains(raw, []byte("\n")) {
		return raw
	}
	out := make([]byte, 0, len(raw)+bytes.Count(raw, []byte("\n")))
	for i, c := range raw {
		if c == '\n' && (i == 0 || raw[i-1] != '\r') {
			out = append(out, '\r')
		}
		out = append(out, c)
	}
	return out
}
