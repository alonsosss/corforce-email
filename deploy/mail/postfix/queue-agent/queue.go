package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	postqueueBin = "/usr/sbin/postqueue"
	postsuperBin = "/usr/sbin/postsuper"

	commandTimeout = 20 * time.Second
	// maxLineBytes acota una linea de `postqueue -j`: un mensaje con miles de destinatarios cabe de
	// sobra y una linea absurda no consume la memoria del contenedor de Postfix.
	maxLineBytes = 4 << 20
	// lineHeadBytes es lo que se conserva de una linea demasiado larga: sobra para la cola, el
	// identificador, la llegada y el tamano, que Postfix escribe primero.
	lineHeadBytes = 4 << 10
	// maxAddress acota una direccion de las que se devuelven (remitente y destinatarios).
	maxAddress = 512
	// maxRecipients acota los destinatarios que se devuelven por mensaje; el total se cuenta aparte.
	maxRecipients = 50
	// maxDelayReason acota el motivo de diferimiento (texto que escribe el servidor remoto).
	maxDelayReason = 300
)

// queueID valida un identificador de cola de Postfix (corto o largo) antes de pasarlo a un comando.
// Es la unica entrada del usuario que llega a un proceso: nada mas que letras y digitos.
var queueID = regexp.MustCompile(`^[0-9A-Za-z]{5,25}$`)

// ValidQueueID dice si id es un identificador de cola aceptable.
func ValidQueueID(id string) bool { return queueID.MatchString(id) }

// Recipient es un destinatario pendiente y por que sigue en cola.
type Recipient struct {
	Address     string `json:"address"`
	DelayReason string `json:"delay_reason,omitempty"`
}

// Message es un mensaje de la cola de Postfix, sin su contenido.
type Message struct {
	QueueID          string      `json:"queue_id"`
	QueueName        string      `json:"queue_name"`
	ArrivalTime      int64       `json:"arrival_time"`
	MessageSize      int64       `json:"message_size"`
	Sender           string      `json:"sender"`
	Recipients       []Recipient `json:"recipients"`
	RecipientsTotal  int         `json:"recipients_total"`
	RecipientsCapped bool        `json:"recipients_capped,omitempty"`
}

// Listing es la respuesta de una consulta a la cola. Counts y OldestArrival describen la cola entera, no
// solo los mensajes devueltos: Counts cuenta por cola de Postfix (incoming, active, deferred, hold) y
// OldestArrival es el instante Unix del mensaje mas antiguo sin contar los retenidos (0 si no hay). Con ellos se vigila la
// cola sin traer los mensajes.
type Listing struct {
	Total         int            `json:"total"`
	Truncated     bool           `json:"truncated"`
	Counts        map[string]int `json:"counts"`
	OldestArrival int64          `json:"oldest_arrival"`
	Items         []Message      `json:"items"`
}

// Queue ejecuta las operaciones de la cola. Los binarios se pasan por campo para que las pruebas
// usen unos falsos; en produccion son los de Postfix.
type Queue struct {
	postqueue string
	postsuper string
}

func NewQueue() *Queue { return &Queue{postqueue: postqueueBin, postsuper: postsuperBin} }

var (
	ErrNotFound = errors.New("el mensaje no esta en la cola")
	ErrCommand  = errors.New("postfix no pudo completar la operacion")
)

// rawMessage es una linea de `postqueue -j` (postqueue(1), formato JSON de Postfix 3.1 o superior).
type rawMessage struct {
	QueueName   string `json:"queue_name"`
	QueueID     string `json:"queue_id"`
	ArrivalTime int64  `json:"arrival_time"`
	MessageSize int64  `json:"message_size"`
	Sender      string `json:"sender"`
	Recipients  []struct {
		Address     string `json:"address"`
		DelayReason string `json:"delay_reason"`
	} `json:"recipients"`
}

// List devuelve hasta limit mensajes de la cola. Cuenta todos aunque devuelva menos: Truncated avisa
// de que hay mas de los que se ven.
func (q *Queue) List(ctx context.Context, limit int) (Listing, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, q.postqueue, "-j")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Listing{}, fmt.Errorf("%w: %v", ErrCommand, err)
	}
	if err := cmd.Start(); err != nil {
		return Listing{}, fmt.Errorf("%w: %v", ErrCommand, err)
	}
	deprioritize(cmd.Process.Pid)
	out, parseErr := parseListing(stdout, limit)
	// El resto de la salida se descarta para que el proceso termine y no quede colgado de la tuberia.
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		return Listing{}, fmt.Errorf("%w: postqueue -j: %v", ErrCommand, err)
	}
	if parseErr != nil {
		return Listing{}, parseErr
	}
	return out, nil
}

// parseListing lee la salida de `postqueue -j` en streaming. Lo unico que crece con la cola es el
// conteo: de cada mensaje mas alla de limit solo se lee la cabecera (cola, identificador, llegada y
// tamano), sin decodificar sus destinatarios.
func parseListing(r io.Reader, limit int) (Listing, error) {
	out := Listing{Items: []Message{}, Counts: map[string]int{}}
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, oversize, err := readLine(br)
		if len(bytes.TrimSpace(line)) > 0 {
			if perr := out.add(line, oversize, limit); perr != nil {
				return Listing{}, perr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return Listing{}, fmt.Errorf("%w: %v", ErrCommand, err)
		}
	}
}

// readLine lee una linea. Si pasa de maxLineBytes (un mensaje con decenas de miles de destinatarios) la
// descarta hasta el salto de linea y devuelve solo su comienzo, con oversize: que un mensaje asi no pueda
// impedir listar la cola es lo que permite encontrarlo y borrarlo.
func readLine(r *bufio.Reader) (line []byte, oversize bool, err error) {
	var buf []byte
	for {
		chunk, rerr := r.ReadSlice('\n')
		if !oversize {
			buf = append(buf, chunk...)
			if len(buf) > maxLineBytes {
				oversize = true
				buf = buf[:lineHeadBytes]
			}
		}
		if errors.Is(rerr, bufio.ErrBufferFull) {
			continue
		}
		return buf, oversize, rerr
	}
}

// add cuenta un mensaje y, si aun no se llego a limit, lo agrega al listado.
func (l *Listing) add(line []byte, oversize bool, limit int) error {
	var raw rawMessage
	full := !oversize && len(l.Items) < limit
	if !full {
		head, ok := readHead(line)
		switch {
		case ok:
			raw = head
		case oversize:
			return nil
		default:
			full = true
		}
	}
	if full {
		if err := json.Unmarshal(line, &raw); err != nil {
			return fmt.Errorf("%w: linea de la cola ilegible", ErrCommand)
		}
	}
	if !ValidQueueID(raw.QueueID) {
		return nil
	}
	l.Total++
	l.Counts[raw.QueueName]++
	// Un mensaje retenido lo dejo alli una persona: no cuenta como cola atascada.
	if raw.QueueName != "hold" && raw.ArrivalTime > 0 && (l.OldestArrival == 0 || raw.ArrivalTime < l.OldestArrival) {
		l.OldestArrival = raw.ArrivalTime
	}
	if len(l.Items) >= limit {
		l.Truncated = true
		return nil
	}
	msg := Message{
		QueueID: raw.QueueID, QueueName: raw.QueueName, ArrivalTime: raw.ArrivalTime,
		MessageSize: raw.MessageSize, Sender: clip(raw.Sender, maxAddress), RecipientsTotal: len(raw.Recipients),
		Recipients: []Recipient{}, RecipientsCapped: oversize,
	}
	for i, rcpt := range raw.Recipients {
		if i >= maxRecipients {
			msg.RecipientsCapped = true
			break
		}
		msg.Recipients = append(msg.Recipients, Recipient{Address: clip(rcpt.Address, maxAddress), DelayReason: clip(rcpt.DelayReason, maxDelayReason)})
	}
	l.Items = append(l.Items, msg)
	return nil
}

var (
	headQueueName = regexp.MustCompile(`"queue_name": ?"([a-z]+)"`)
	headQueueID   = regexp.MustCompile(`"queue_id": ?"([0-9A-Za-z]+)"`)
	headArrival   = regexp.MustCompile(`"arrival_time": ?(\d+)`)
	headSize      = regexp.MustCompile(`"message_size": ?(\d+)`)
)

// readHead saca de los primeros bytes de una linea lo que Postfix escribe antes del remitente y los
// destinatarios (que son lo que un atacante controla). ok es false si falta cualquiera de los tres
// primeros campos.
func readHead(line []byte) (rawMessage, bool) {
	head := line[:min(len(line), lineHeadBytes)]
	name, id, arrival := headQueueName.FindSubmatch(head), headQueueID.FindSubmatch(head), headArrival.FindSubmatch(head)
	if name == nil || id == nil || arrival == nil {
		return rawMessage{}, false
	}
	raw := rawMessage{QueueName: string(name[1]), QueueID: string(id[1])}
	raw.ArrivalTime, _ = strconv.ParseInt(string(arrival[1]), 10, 64)
	if size := headSize.FindSubmatch(head); size != nil {
		raw.MessageSize, _ = strconv.ParseInt(string(size[1]), 10, 64)
	}
	return raw, true
}

func clip(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// Action es lo unico que se puede hacer con un mensaje de la cola.
type Action string

const (
	ActionRetry  Action = "retry"
	ActionHold   Action = "hold"
	ActionUnhold Action = "unhold"
	ActionDelete Action = "delete"
)

// ValidAction dice si a es una accion admitida.
func ValidAction(a Action) bool {
	switch a {
	case ActionRetry, ActionHold, ActionUnhold, ActionDelete:
		return true
	}
	return false
}

var affected = regexp.MustCompile(`: (\d+) messages?\b`)

// Apply hace action sobre el mensaje id. El identificador se valida aqui otra vez aunque el
// llamador ya lo haya hecho: es lo unico que se pasa a un proceso. Devuelve ErrNotFound si Postfix
// no encontro el mensaje.
func (q *Queue) Apply(ctx context.Context, action Action, id string) error {
	if !ValidQueueID(id) || !ValidAction(action) {
		return ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	var cmd *exec.Cmd
	switch action {
	case ActionRetry:
		cmd = exec.CommandContext(ctx, q.postqueue, "-i", id)
	case ActionHold:
		cmd = exec.CommandContext(ctx, q.postsuper, "-h", id)
	case ActionUnhold:
		cmd = exec.CommandContext(ctx, q.postsuper, "-H", id)
	case ActionDelete:
		cmd = exec.CommandContext(ctx, q.postsuper, "-d", id)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s %s: %v", ErrCommand, action, id, err)
	}
	if action == ActionRetry {
		return nil
	}
	if m := affected.FindSubmatch(out); m != nil {
		if n, _ := strconv.Atoi(string(m[1])); n == 0 {
			return ErrNotFound
		}
	}
	return nil
}

// Flush pide a Postfix reintentar toda la cola diferida (postqueue -f).
func (q *Queue) Flush(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, q.postqueue, "-f").CombinedOutput(); err != nil {
		return fmt.Errorf("%w: postqueue -f: %v (%s)", ErrCommand, err, strings.TrimSpace(clip(string(out), 200)))
	}
	return nil
}
