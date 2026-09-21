package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// ReasonCheckpointMismatch: la fila en la que una verificacion anterior dejo su punto ya no
// contiene el hash que se registro (se editaron o se borraron filas ya verificadas).
const ReasonCheckpointMismatch = "checkpoint_mismatch"

// ChainCheckpoint es el punto hasta el que una cadena esta verificada y lo que hace falta para
// seguir desde ahi sin releer lo anterior. Hash es el hash de la fila de posicion Seq: la
// verificacion que continua relee esa fila, comprueba su contenido y que su hash es este, y solo
// entonces exige que la siguiente enlace con el. Atar el punto a la fila y no solo a un numero es
// lo que impide que una reanudacion se salte una edicion de esa fila.
type ChainCheckpoint struct {
	Seq         int64          `json:"seq"`
	Hash        string         `json:"hash"`
	HashVersion int            `json:"hash_version"`
	SawKeyed    bool           `json:"saw_keyed"`
	Checked     int            `json:"checked"`
	Versions    map[string]int `json:"versions,omitempty"`
	// Complete marca el punto final: la cadena se recorrio hasta el final, no solo hasta un lote.
	Complete bool `json:"complete,omitempty"`
}

// VerifyOptions gobierna una verificacion de cadena. From reanuda desde un punto (nil: desde el
// principio). OnBatch se llama tras cada lote verificado con el punto alcanzado; si devuelve un
// error, la verificacion se detiene y lo devuelve tal cual.
type VerifyOptions struct {
	From    *ChainCheckpoint
	OnBatch func(ChainCheckpoint) error
}

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunCancelled RunStatus = "cancelled"
	RunFailed    RunStatus = "failed"
)

// RunMode: full verifica la cadena entera; incremental parte del punto de la ultima verificacion
// completa que dio OK y solo relee lo posterior.
type RunMode string

const (
	RunModeFull        RunMode = "full"
	RunModeIncremental RunMode = "incremental"
)

func (m RunMode) Valid() bool { return m == RunModeFull || m == RunModeIncremental }

type RunTrigger string

const (
	RunTriggerManual RunTrigger = "manual"
	RunTriggerSweep  RunTrigger = "sweep"
	// RunTriggerRequest es la que arranca GET /integrity cuando la cadena es demasiado grande para
	// contestar dentro de la peticion.
	RunTriggerRequest RunTrigger = "request"
)

// Desenlaces de una verificacion, para las metricas: la cadena esta bien, esta rota, se cancelo o
// fallo por una causa tecnica (que no dice nada de la cadena).
const (
	RunOutcomeOK        = "ok"
	RunOutcomeBroken    = "broken"
	RunOutcomeCancelled = "cancelled"
	RunOutcomeFailed    = "failed"
)

// Fases de una verificacion, en el orden en que se recorren.
const (
	RunPhaseAuditLogs      = string(ChainAuditLogs)
	RunPhaseSecurityEvents = string(ChainSecurityEvents)
	RunPhaseAnchors        = "anchors"
	RunPhaseDone           = "done"
)

// Codigos de fallo tecnico de una verificacion (no es lo mismo que una cadena rota: ahi la
// verificacion termino y el veredicto esta en Result).
const (
	RunErrorInternal = "internal_error"
	RunErrorTimeout  = "timeout"
)

// ChainState es lo que una verificacion sabe de una cadena: hasta donde llego y, si ya la termino,
// su resultado (acierte o falle).
type ChainState struct {
	Checkpoint *ChainCheckpoint `json:"checkpoint,omitempty"`
	Result     *ChainIntegrity  `json:"result,omitempty"`
}

// IntegrityRun es una verificacion de las cadenas de una empresa que corre en segundo plano.
// Owner identifica el proceso que la ejecuta; el latido (HeartbeatAt) dice si sigue vivo.
type IntegrityRun struct {
	ID          uuid.UUID  `json:"id"`
	TenantID    uuid.UUID  `json:"-"`
	Mode        RunMode    `json:"mode"`
	Trigger     RunTrigger `json:"trigger"`
	RequestedBy *uuid.UUID `json:"requested_by,omitempty"`
	Status      RunStatus  `json:"status"`
	Phase       string     `json:"phase"`
	// Checked cuenta las filas verificadas de las dos cadenas; CurrentSeq y TargetSeq son la
	// posicion alcanzada en audit_logs y la de su cabeza al empezar: dan el avance sin contar la
	// tabla (con huecos en seq, es una cota, no un porcentaje exacto).
	Checked         int64                     `json:"checked"`
	CurrentSeq      int64                     `json:"current_seq"`
	TargetSeq       int64                     `json:"target_seq"`
	CancelRequested bool                      `json:"cancel_requested"`
	Result          *ChainIntegrity           `json:"result,omitempty"`
	ErrorCode       string                    `json:"error,omitempty"`
	StartedAt       time.Time                 `json:"started_at"`
	HeartbeatAt     time.Time                 `json:"heartbeat_at"`
	FinishedAt      *time.Time                `json:"finished_at,omitempty"`
	Owner           string                    `json:"-"`
	Chains          map[ChainName]*ChainState `json:"-"`
}

func (r *IntegrityRun) Finished() bool { return r.Status != RunRunning }

// OK dice si la verificacion termino y la cadena esta bien.
func (r *IntegrityRun) OK() bool {
	return r.Status == RunCompleted && r.Result != nil && r.Result.OK
}

// RunProgress es lo que una verificacion en curso deja escrito: la fase, el avance y el estado de
// cada cadena para poder reanudar.
type RunProgress struct {
	Phase      string
	Checked    int64
	CurrentSeq int64
	Chains     map[ChainName]*ChainState
}

// RunFinish cierra una verificacion.
type RunFinish struct {
	Status    RunStatus
	Result    *ChainIntegrity
	ErrorCode string
	Chains    map[ChainName]*ChainState
}

var (
	// ErrRunNotFound: la verificacion no existe o es de otra empresa.
	ErrRunNotFound = errors.New("integrity run not found")
	// ErrRunActive: la empresa ya tiene una verificacion en curso. Va acompanada de esa
	// verificacion.
	ErrRunActive = errors.New("integrity run already active")
	// ErrRunLost: otro proceso tomo la verificacion (su latido vencio) y este ya no es su dueno.
	ErrRunLost = errors.New("integrity run taken over by another process")
	// ErrRunCancelled: se pidio cancelar la verificacion.
	ErrRunCancelled = errors.New("integrity run cancelled")
)
