package domain

import (
	"strconv"
	"strings"
)

// ListView es como se agrupa el listado de una carpeta.
type ListView string

const (
	ViewMessages ListView = "messages"
	ViewThreads  ListView = "threads"
)

// ParseListView interpreta ?view=; vacio es la vista por mensajes de siempre.
func ParseListView(raw string) (ListView, error) {
	switch raw {
	case "", string(ViewMessages):
		return ViewMessages, nil
	case string(ViewThreads):
		return ViewThreads, nil
	}
	return "", invalid("view", "debe ser messages o threads")
}

// Topes de las conversaciones.
const (
	// ThreadScanLimit acota la agrupacion por References e In-Reply-To cuando el servidor no hace
	// THREAD=REFERENCES: solo se agrupan los mensajes mas recientes y el total se declara acotado.
	ThreadScanLimit = 2000
	// MaxThreadMessages es lo que se devuelve de una conversacion (sus UIDs en el listado y sus
	// mensajes al abrirla), de los mas recientes a los mas antiguos.
	MaxThreadMessages = 200
	// maxThreadParticipants acota los remitentes que resume una fila.
	maxThreadParticipants = 5
	// MaxRelatedMessageIDs acota los identificadores con los que se buscan en Enviados las
	// respuestas propias de una conversacion.
	MaxRelatedMessageIDs = 25
)

// ThreadMessage es lo que hace falta de un mensaje para encadenarlo: sus identificadores.
type ThreadMessage struct {
	UID        uint32
	MessageID  string
	InReplyTo  []string
	References []string
}

// GroupThreads agrupa los mensajes por References e In-Reply-To (RFC 5256, sin la parte de los
// asuntos, que junta conversaciones distintas con el mismo "Re: presupuesto"). Dos mensajes van
// juntos si uno nombra al otro o si nombran a un tercero comun, aunque ese tercero no este en la
// carpeta. Cada grupo conserva el orden de entrada de sus UIDs, y los grupos salen en el orden de
// su primer mensaje: con la entrada del mas reciente al mas antiguo, el listado queda por la
// ultima actividad.
func GroupThreads(msgs []ThreadMessage) [][]uint32 {
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	ensure := func(x string) {
		if _, ok := parent[x]; !ok {
			parent[x] = x
		}
	}
	union := func(a, b string) {
		ensure(a)
		ensure(b)
		if ra, rb := find(a), find(b); ra != rb {
			parent[rb] = ra
		}
	}
	keys := make([]string, len(msgs))
	for i, m := range msgs {
		// Un mensaje sin Message-ID solo se agrupa por lo que el nombra.
		own := "uid:" + strconv.FormatUint(uint64(m.UID), 10)
		if id := normalizeMessageID(m.MessageID); id != "" {
			own = "id:" + id
		}
		keys[i] = own
		ensure(own)
		for _, ref := range append(append([]string(nil), m.References...), m.InReplyTo...) {
			if id := normalizeMessageID(ref); id != "" {
				union(own, "id:"+id)
			}
		}
	}
	index := map[string]int{}
	var out [][]uint32
	for i, m := range msgs {
		root := find(keys[i])
		at, ok := index[root]
		if !ok {
			at = len(out)
			index[root] = at
			out = append(out, nil)
		}
		out[at] = append(out[at], m.UID)
	}
	return out
}

func normalizeMessageID(id string) string {
	id = strings.Trim(strings.TrimSpace(id), "<>")
	if !IsValidMessageID(id) {
		return ""
	}
	return strings.ToLower(id)
}

// ThreadSummary es una fila del listado por conversaciones: el ultimo mensaje y el resumen del
// resto. UIDs va del mas reciente al mas antiguo y se acota a MaxThreadMessages; Size cuenta todos.
type ThreadSummary struct {
	Latest       Envelope
	UIDs         []uint32
	Size         int
	Unread       int
	Participants []Address
}

// ThreadPage es una pagina de conversaciones. Capped: la agrupacion solo recorrio los mensajes mas
// recientes (el servidor no agrupa por si mismo).
type ThreadPage struct {
	Items  []ThreadSummary
	Total  int
	Capped bool
}

// ConversationMessage es un mensaje de una conversacion abierta, con la carpeta donde esta (las
// respuestas propias viven en Enviados).
type ConversationMessage struct {
	Folder    string
	MessageID string
	Envelope
}

// Participants resume los remitentes de una conversacion sin repetir, en el orden recibido.
func Participants(envs []Envelope) []Address {
	seen := map[string]bool{}
	var out []Address
	for _, e := range envs {
		for _, a := range e.From {
			key := strings.ToLower(a.Email)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, a)
			if len(out) == maxThreadParticipants {
				return out
			}
		}
	}
	return out
}
