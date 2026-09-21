package main

import (
	"bufio"
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

// Listing es la respuesta de una consulta a la cola.
type Listing struct {
	Total     int       `json:"total"`
	Truncated bool      `json:"truncated"`
	Items     []Message `json:"items"`
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

func parseListing(r io.Reader, limit int) (Listing, error) {
	out := Listing{Items: []Message{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var raw rawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return Listing{}, fmt.Errorf("%w: linea de la cola ilegible", ErrCommand)
		}
		if !ValidQueueID(raw.QueueID) {
			continue
		}
		out.Total++
		if len(out.Items) >= limit {
			out.Truncated = true
			continue
		}
		msg := Message{
			QueueID: raw.QueueID, QueueName: raw.QueueName, ArrivalTime: raw.ArrivalTime,
			MessageSize: raw.MessageSize, Sender: raw.Sender, RecipientsTotal: len(raw.Recipients),
			Recipients: []Recipient{},
		}
		for i, rcpt := range raw.Recipients {
			if i >= maxRecipients {
				msg.RecipientsCapped = true
				break
			}
			msg.Recipients = append(msg.Recipients, Recipient{Address: rcpt.Address, DelayReason: clip(rcpt.DelayReason, maxDelayReason)})
		}
		out.Items = append(out.Items, msg)
	}
	if err := sc.Err(); err != nil {
		return Listing{}, fmt.Errorf("%w: %v", ErrCommand, err)
	}
	return out, nil
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
