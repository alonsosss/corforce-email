package domain

// MaxBatchUIDs acota los mensajes de una accion en lote: cada UID viaja en el comando IMAP y la
// interfaz lo recibe en /meta.
const MaxBatchUIDs = 500

// BatchAction es lo que se hace con los mensajes seleccionados.
type BatchAction string

const (
	BatchFlags  BatchAction = "flags"
	BatchMove   BatchAction = "move"
	BatchDelete BatchAction = "delete"
)

// Batch es una accion validada sobre varios mensajes de una carpeta.
type Batch struct {
	UIDs   []uint32
	Action BatchAction
	Flags  FlagChange
	To     string
}

// BatchResult cuenta los mensajes que existian y se tocaron. Permanent solo en un borrado desde
// la papelera.
type BatchResult struct {
	Affected  int
	Permanent bool
}

// NewBatch valida la accion pedida sobre la carpeta folder: UIDs positivos sin repetir y hasta
// MaxBatchUIDs, y los datos propios de cada accion.
func NewBatch(folder string, uids []uint32, action string, add, remove []string, to string) (Batch, error) {
	if len(uids) == 0 {
		return Batch{}, invalid("uids", "hace falta al menos un mensaje")
	}
	if len(uids) > MaxBatchUIDs {
		return Batch{}, invalid("uids", "demasiados mensajes en una sola acción")
	}
	seen := make(map[uint32]bool, len(uids))
	out := make([]uint32, 0, len(uids))
	for _, uid := range uids {
		if uid == 0 {
			return Batch{}, invalid("uids", "cada UID debe ser un entero positivo")
		}
		if !seen[uid] {
			seen[uid] = true
			out = append(out, uid)
		}
	}
	b := Batch{UIDs: out, Action: BatchAction(action)}
	switch b.Action {
	case BatchFlags:
		change, err := NewFlagChange(add, remove)
		if err != nil {
			return Batch{}, err
		}
		b.Flags = change
	case BatchMove:
		if err := ValidateFolderName(to); err != nil {
			return Batch{}, asField(err, "to")
		}
		if to == folder {
			return Batch{}, invalid("to", "los mensajes ya están en esa carpeta")
		}
		b.To = to
	case BatchDelete:
	default:
		return Batch{}, invalid("action", "debe ser flags, move o delete")
	}
	return b, nil
}
