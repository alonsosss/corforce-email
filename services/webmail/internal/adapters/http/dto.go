package http

import (
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/app"
	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

type quotaDTO struct {
	UsedBytes  int64 `json:"used_bytes"`
	LimitBytes int64 `json:"limit_bytes"`
}

type sessionDTO struct {
	Username           string    `json:"username"`
	DisplayName        string    `json:"display_name"`
	ExpiresAt          string    `json:"expires_at"`
	IdleTimeoutSeconds int64     `json:"idle_timeout_seconds"`
	Quota              *quotaDTO `json:"quota"`
}

func toSessionDTO(s domain.Session, cfg Config, q *domain.Quota) sessionDTO {
	dto := sessionDTO{
		Username:           s.Username,
		DisplayName:        s.DisplayName,
		ExpiresAt:          s.ExpiresAt.UTC().Format(time.RFC3339),
		IdleTimeoutSeconds: int64(cfg.SessionIdle.Seconds()),
	}
	if q != nil {
		dto.Quota = &quotaDTO{UsedBytes: q.UsedBytes, LimitBytes: q.LimitBytes}
	}
	return dto
}

type folderDTO struct {
	Name       string `json:"name"`
	Delimiter  string `json:"delimiter"`
	Role       string `json:"role"`
	Selectable bool   `json:"selectable"`
	Total      uint32 `json:"total"`
	Unread     uint32 `json:"unread"`
}

func toFolderDTO(f domain.Folder) folderDTO {
	return folderDTO{Name: f.Name, Delimiter: f.Delimiter, Role: string(f.Role), Selectable: f.Selectable, Total: f.Total, Unread: f.Unread}
}

func toFolderDTOs(folders []domain.Folder) []folderDTO {
	out := make([]folderDTO, len(folders))
	for i, f := range folders {
		out[i] = toFolderDTO(f)
	}
	return out
}

type addressDTO struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func toAddressDTOs(list []domain.Address) []addressDTO {
	out := make([]addressDTO, len(list))
	for i, a := range list {
		out[i] = addressDTO{Name: a.Name, Email: a.Email}
	}
	return out
}

type envelopeDTO struct {
	UID            uint32       `json:"uid"`
	From           []addressDTO `json:"from"`
	To             []addressDTO `json:"to"`
	Cc             []addressDTO `json:"cc"`
	Subject        string       `json:"subject"`
	Date           *string      `json:"date"`
	Flags          []string     `json:"flags"`
	Size           int64        `json:"size"`
	HasAttachments bool         `json:"has_attachments"`
}

func toEnvelopeDTO(e domain.Envelope) envelopeDTO {
	return envelopeDTO{
		UID:            e.UID,
		From:           toAddressDTOs(e.From),
		To:             toAddressDTOs(e.To),
		Cc:             toAddressDTOs(e.Cc),
		Subject:        e.Subject,
		Date:           formatDate(e.Date),
		Flags:          toFlagStrings(e.Flags),
		Size:           e.Size,
		HasAttachments: e.HasAttachments,
	}
}

func toEnvelopeDTOs(list []domain.Envelope) []envelopeDTO {
	out := make([]envelopeDTO, len(list))
	for i, e := range list {
		out[i] = toEnvelopeDTO(e)
	}
	return out
}

type partDTO struct {
	Part        string `json:"part"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	ContentID   string `json:"content_id"`
	Inline      bool   `json:"inline"`
}

type remoteImagesDTO struct {
	Present bool `json:"present"`
	Blocked bool `json:"blocked"`
}

type messageDTO struct {
	envelopeDTO
	Folder        string          `json:"folder"`
	Bcc           []addressDTO    `json:"bcc"`
	ReplyTo       []addressDTO    `json:"reply_to"`
	MessageID     string          `json:"message_id"`
	InReplyTo     []string        `json:"in_reply_to"`
	References    []string        `json:"references"`
	Text          string          `json:"text"`
	TextTruncated bool            `json:"text_truncated"`
	HTML          string          `json:"html"`
	HTMLTruncated bool            `json:"html_truncated"`
	RemoteImages  remoteImagesDTO `json:"remote_images"`
	Attachments   []partDTO       `json:"attachments"`
}

func toMessageDTO(m *domain.Message) messageDTO {
	parts := make([]partDTO, len(m.Attachments))
	for i, p := range m.Attachments {
		parts[i] = partDTO{Part: p.ID, Filename: p.Filename, ContentType: p.ContentType, Size: p.Size, ContentID: p.ContentID, Inline: p.Inline}
	}
	return messageDTO{
		envelopeDTO:   toEnvelopeDTO(m.Envelope),
		Folder:        m.Folder,
		Bcc:           toAddressDTOs(m.Bcc),
		ReplyTo:       toAddressDTOs(m.ReplyTo),
		MessageID:     m.MessageID,
		InReplyTo:     nonNil(m.InReplyTo),
		References:    nonNil(m.References),
		Text:          m.Text,
		TextTruncated: m.TextTruncated,
		HTML:          m.HTML,
		HTMLTruncated: m.HTMLTruncated,
		RemoteImages:  remoteImagesDTO{Present: m.RemoteImages.Present, Blocked: m.RemoteImages.Blocked},
		Attachments:   parts,
	}
}

type deleteDTO struct {
	Permanent bool `json:"permanent"`
}

type sendDTO struct {
	MessageID    string `json:"message_id"`
	SavedToSent  bool   `json:"saved_to_sent"`
	DraftRemoved bool   `json:"draft_removed"`
	Replayed     bool   `json:"replayed"`
}

type metaDTO struct {
	Limits       metaLimitsDTO     `json:"limits"`
	Pagination   metaPaginationDTO `json:"pagination"`
	FolderRoles  []string          `json:"folder_roles"`
	MutableFlags []string          `json:"mutable_flags"`
	Session      metaSessionDTO    `json:"session"`
}

type metaLimitsDTO struct {
	MaxRecipients      int   `json:"max_recipients"`
	MaxMessageBytes    int64 `json:"max_message_bytes"`
	MaxAttachments     int   `json:"max_attachments"`
	MaxDownloadBytes   int64 `json:"max_download_bytes"`
	MaxBodyPartBytes   int64 `json:"max_body_part_bytes"`
	MaxSubjectChars    int   `json:"max_subject_chars"`
	MaxSearchBytes     int   `json:"max_search_bytes"`
	MaxFolderNameBytes int   `json:"max_folder_name_bytes"`
	MaxBatchUIDs       int   `json:"max_batch_uids"`
	MaxScheduledDays   int   `json:"max_scheduled_days"`
	MaxImportBytes     int64 `json:"max_import_bytes"`
}

type metaPaginationDTO struct {
	DefaultPageSize int `json:"default_page_size"`
	MaxPageSize     int `json:"max_page_size"`
}

type metaSessionDTO struct {
	IdleTimeoutSeconds int64 `json:"idle_timeout_seconds"`
	MaxLifetimeSeconds int64 `json:"max_lifetime_seconds"`
}

func toMetaDTO(m app.Meta) metaDTO {
	roles := make([]string, len(m.FolderRoles))
	for i, r := range m.FolderRoles {
		roles[i] = string(r)
	}
	return metaDTO{
		Limits: metaLimitsDTO{
			MaxRecipients: m.MaxRecipients, MaxMessageBytes: m.MaxMessageBytes, MaxAttachments: m.MaxAttachments,
			MaxDownloadBytes: m.MaxDownloadBytes, MaxBodyPartBytes: m.MaxBodyPartBytes, MaxSubjectChars: m.MaxSubjectChars,
			MaxSearchBytes: m.MaxSearchBytes, MaxFolderNameBytes: m.MaxFolderNameBytes,
			MaxBatchUIDs: m.MaxBatchUIDs, MaxScheduledDays: m.MaxScheduledDays, MaxImportBytes: m.MaxImportBytes,
		},
		Pagination:   metaPaginationDTO{DefaultPageSize: m.DefaultPageSize, MaxPageSize: m.MaxPageSize},
		FolderRoles:  roles,
		MutableFlags: toFlagStrings(m.MutableFlags),
		Session: metaSessionDTO{
			IdleTimeoutSeconds: int64(m.SessionIdle.Seconds()), MaxLifetimeSeconds: int64(m.SessionMax.Seconds()),
		},
	}
}

type identityDTO struct {
	Email   string `json:"email"`
	Name    string `json:"name"`
	Primary bool   `json:"primary"`
}

func toIdentityDTOs(list []domain.SenderIdentity) []identityDTO {
	out := make([]identityDTO, len(list))
	for i, id := range list {
		out[i] = identityDTO{Email: id.Address, Name: id.Name, Primary: id.Primary}
	}
	return out
}

type draftDTO struct {
	UID uint32 `json:"uid"`
}

func toFlagStrings(flags []domain.Flag) []string {
	out := make([]string, len(flags))
	for i, f := range flags {
		out[i] = string(f)
	}
	return out
}

func formatDate(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

type vacationLimitsDTO struct {
	SubjectMaxLength int `json:"subject_max_length"`
	MessageMaxLength int `json:"message_max_length"`
	IntervalMinDays  int `json:"interval_min_days"`
	IntervalMaxDays  int `json:"interval_max_days"`
}

type vacationDTO struct {
	Enabled      bool              `json:"enabled"`
	Subject      string            `json:"subject"`
	Message      string            `json:"message"`
	IntervalDays int               `json:"interval_days"`
	StartsOn     *string           `json:"starts_on"`
	EndsOn       *string           `json:"ends_on"`
	UpdatedAt    *time.Time        `json:"updated_at"`
	Limits       vacationLimitsDTO `json:"limits"`
}

func toVacationDTO(v domain.Vacation) vacationDTO {
	return vacationDTO{
		Enabled: v.Enabled, Subject: v.Subject, Message: v.Message, IntervalDays: v.IntervalDays,
		StartsOn: v.StartsOn, EndsOn: v.EndsOn, UpdatedAt: v.UpdatedAt,
		Limits: vacationLimitsDTO{
			SubjectMaxLength: v.Limits.SubjectMaxLength, MessageMaxLength: v.Limits.MessageMaxLength,
			IntervalMinDays: v.Limits.IntervalMinDays, IntervalMaxDays: v.Limits.IntervalMaxDays,
		},
	}
}

type vacationRequest struct {
	Enabled      bool    `json:"enabled"`
	Subject      string  `json:"subject"`
	Message      string  `json:"message"`
	IntervalDays int     `json:"interval_days"`
	StartsOn     *string `json:"starts_on"`
	EndsOn       *string `json:"ends_on"`
}
