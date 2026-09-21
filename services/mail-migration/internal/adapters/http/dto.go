package http

import (
	"strconv"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-migration/internal/app"
	"github.com/alonsosss/corforce-email/services/mail-migration/internal/domain"
)

type progressDTO struct {
	FoldersTotal    int64       `json:"folders_total"`
	FoldersDone     int64       `json:"folders_done"`
	MessagesTotal   int64       `json:"messages_total"`
	MessagesCopied  int64       `json:"messages_copied"`
	MessagesSkipped int64       `json:"messages_skipped"`
	MessagesFailed  int64       `json:"messages_failed"`
	BytesCopied     int64       `json:"bytes_copied"`
	Folders         []folderDTO `json:"folders"`
}

type folderDTO struct {
	Name            string `json:"name"`
	MessagesCopied  int64  `json:"messages_copied"`
	MessagesSkipped int64  `json:"messages_skipped"`
	MessagesFailed  int64  `json:"messages_failed"`
}

type errorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// jobDTO es lo unico que sale de un trabajo por la API: nunca la contrasena de origen, cifrada o no,
// ni el lease del ejecutor.
type jobDTO struct {
	ID                string      `json:"id"`
	MailboxID         string      `json:"mailbox_id"`
	MailboxUsername   string      `json:"mailbox_username"`
	SourceHost        string      `json:"source_host"`
	SourcePort        int         `json:"source_port"`
	SourceTLS         string      `json:"source_tls"`
	SourceUsername    string      `json:"source_username"`
	Status            string      `json:"status"`
	Phase             string      `json:"phase"`
	Progress          progressDTO `json:"progress"`
	Attempt           int         `json:"attempt"`
	LastError         *errorDTO   `json:"last_error"`
	RequestedBy       string      `json:"requested_by"`
	CreatedAt         time.Time   `json:"created_at"`
	StartedAt         *time.Time  `json:"started_at"`
	FinishedAt        *time.Time  `json:"finished_at"`
	CancelRequestedAt *time.Time  `json:"cancel_requested_at"`
	HeartbeatAt       *time.Time  `json:"heartbeat_at"`
}

func toJobDTO(j *domain.Job) jobDTO {
	folders := make([]folderDTO, len(j.Progress.Folders))
	for i, f := range j.Progress.Folders {
		folders[i] = folderDTO(f)
	}
	out := jobDTO{
		ID: j.ID.String(), MailboxID: j.MailboxID.String(), MailboxUsername: j.MailboxUsername,
		SourceHost: j.SourceHost, SourcePort: j.SourcePort, SourceTLS: string(j.SourceTLS), SourceUsername: j.SourceUsername,
		Status: string(j.Status), Phase: string(j.Phase), Attempt: j.Attempt, RequestedBy: j.RequestedBy.String(),
		CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
		CancelRequestedAt: j.CancelRequestedAt, HeartbeatAt: j.HeartbeatAt,
		Progress: progressDTO{
			FoldersTotal: j.Progress.FoldersTotal, FoldersDone: j.Progress.FoldersDone, MessagesTotal: j.Progress.MessagesTotal,
			MessagesCopied: j.Progress.MessagesCopied, MessagesSkipped: j.Progress.MessagesSkipped,
			MessagesFailed: j.Progress.MessagesFailed, BytesCopied: j.Progress.BytesCopied, Folders: folders,
		},
	}
	if j.LastError != nil {
		out.LastError = &errorDTO{Code: string(j.LastError.Code), Message: j.LastError.Message}
	}
	return out
}

type metaDTO struct {
	Configured        bool              `json:"configured"`
	SourcePorts       []int             `json:"source_ports"`
	SourceTLSModes    []string          `json:"source_tls_modes"`
	DefaultTLSForPort map[string]string `json:"default_tls_for_port"`
	MaxActiveJobs     int               `json:"max_active_jobs"`
	ActiveJobs        int               `json:"active_jobs"`
	Phases            []string          `json:"phases"`
}

func toMetaDTO(m app.Meta) metaDTO {
	modes := make([]string, len(m.SourceTLSModes))
	for i, t := range m.SourceTLSModes {
		modes[i] = string(t)
	}
	defaults := make(map[string]string, len(m.DefaultTLSForPort))
	for port, t := range m.DefaultTLSForPort {
		defaults[strconv.Itoa(port)] = string(t)
	}
	phases := make([]string, len(m.Phases))
	for i, p := range m.Phases {
		phases[i] = string(p)
	}
	return metaDTO{
		Configured: m.Configured, SourcePorts: m.SourcePorts, SourceTLSModes: modes, DefaultTLSForPort: defaults,
		MaxActiveJobs: m.MaxActiveJobs, ActiveJobs: m.ActiveJobs, Phases: phases,
	}
}
