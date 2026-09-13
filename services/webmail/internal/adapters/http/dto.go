package http

import (
	"time"

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

func toFolderDTOs(folders []domain.Folder) []folderDTO {
	out := make([]folderDTO, len(folders))
	for i, f := range folders {
		out[i] = folderDTO{Name: f.Name, Delimiter: f.Delimiter, Role: string(f.Role), Selectable: f.Selectable, Total: f.Total, Unread: f.Unread}
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
	MessageID   string `json:"message_id"`
	SavedToSent bool   `json:"saved_to_sent"`
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
