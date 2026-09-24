package imap

import (
	"bufio"
	"bytes"
	"io"
	"mime"
	"sort"
	"strings"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	imaplib "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomessage "github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"
)

const (
	maxParts      = 200
	maxReferences = 50
)

// leaf es una parte simple del mensaje con su numero de seccion.
type leaf struct {
	path []int
	part *imaplib.BodyStructureSinglePart
}

// bodyPlan es lo que se muestra de un mensaje: el primer texto y el primer HTML que no
// son adjuntos, y el resto de partes como descargables.
type bodyPlan struct {
	text  *leaf
	html  *leaf
	parts []domain.Part
}

func planBody(bs imaplib.BodyStructure) bodyPlan {
	var plan bodyPlan
	bs.Walk(func(path []int, node imaplib.BodyStructure) bool {
		single, ok := node.(*imaplib.BodyStructureSinglePart)
		if !ok {
			return true
		}
		p := append([]int(nil), path...)
		mediaType := single.MediaType()
		if !isAttachment(single) && filenameOf(single) == "" {
			switch {
			case mediaType == "text/plain" && plan.text == nil:
				plan.text = &leaf{path: p, part: single}
				return true
			case mediaType == "text/html" && plan.html == nil:
				plan.html = &leaf{path: p, part: single}
				return true
			}
		}
		if len(plan.parts) < maxParts {
			plan.parts = append(plan.parts, partOf(p, single))
		}
		return true
	})
	return plan
}

func partOf(path []int, single *imaplib.BodyStructureSinglePart) domain.Part {
	contentID := strings.Trim(strings.TrimSpace(single.ID), "<>")
	return domain.Part{
		ID:          domain.FormatPartID(path),
		ContentType: single.MediaType(),
		Filename:    filenameOf(single),
		Size:        decodedSize(single),
		ContentID:   contentID,
		Inline:      !isAttachment(single) && contentID != "",
	}
}

// findLeaf localiza la parte simple con ese numero de seccion.
func findLeaf(bs imaplib.BodyStructure, target []int) *imaplib.BodyStructureSinglePart {
	var found *imaplib.BodyStructureSinglePart
	bs.Walk(func(path []int, node imaplib.BodyStructure) bool {
		if found != nil {
			return false
		}
		if single, ok := node.(*imaplib.BodyStructureSinglePart); ok && equalPath(path, target) {
			found = single
		}
		return true
	})
	return found
}

func equalPath(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func disposition(single *imaplib.BodyStructureSinglePart) string {
	if d := single.Disposition(); d != nil {
		return strings.ToLower(d.Value)
	}
	return ""
}

func isAttachment(single *imaplib.BodyStructureSinglePart) bool {
	return disposition(single) == "attachment" || single.MediaType() == "message/rfc822"
}

// hasAttachments marca el sobre: una parte adjunta, un mensaje reenviado o un fichero
// con nombre que no se declara en linea.
func hasAttachments(bs imaplib.BodyStructure) bool {
	found := false
	bs.Walk(func(_ []int, node imaplib.BodyStructure) bool {
		single, ok := node.(*imaplib.BodyStructureSinglePart)
		if !ok {
			return !found
		}
		if isAttachment(single) || (filenameOf(single) != "" && disposition(single) != "inline") {
			found = true
		}
		return !found
	})
	return found
}

// filenameOf lee el nombre del fichero de Content-Disposition o, en su defecto, del
// parametro name de Content-Type, incluida la forma extendida de RFC 2231.
func filenameOf(single *imaplib.BodyStructureSinglePart) string {
	var dispParams map[string]string
	if single.Extended != nil && single.Extended.Disposition != nil {
		dispParams = single.Extended.Disposition.Params
	}
	if name := decodedParam(dispParams, "filename"); name != "" {
		return name
	}
	return decodedParam(single.Params, "name")
}

// decodedParam devuelve el parametro en claro. Los servidores entregan en BODYSTRUCTURE
// la forma extendida tal cual (name*=utf-8”..., o partida en name*0*, name*1*), y es
// mime.ParseMediaType quien sabe reensamblarla.
func decodedParam(params map[string]string, name string) string {
	if v := strings.TrimSpace(params[name]); v != "" {
		return v
	}
	var keys []string
	for k := range params {
		if strings.HasPrefix(k, name+"*") {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("x")
	for _, k := range keys {
		v := params[k]
		b.WriteString("; " + k + "=")
		if strings.HasSuffix(k, "*") {
			b.WriteString(v)
		} else {
			b.WriteString(`"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`)
		}
	}
	_, parsed, err := mime.ParseMediaType(b.String())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parsed[name])
}

// decodedSize estima el tamano decodificado a partir del codificado.
func decodedSize(single *imaplib.BodyStructureSinglePart) int64 {
	if strings.EqualFold(single.Encoding, "base64") {
		return int64(single.Size) * 3 / 4
	}
	return int64(single.Size)
}

// decodeText decodifica una parte de texto (transferencia y juego de caracteres) y la
// deja en UTF-8 valido, acotada a maxBytes.
func decodeText(raw []byte, single *imaplib.BodyStructureSinglePart, maxBytes int64, encodedTruncated bool) (string, bool) {
	h := gomessage.Header{}
	params := map[string]string{}
	if cs := single.Params["charset"]; cs != "" {
		params["charset"] = cs
	}
	h.SetContentType(single.MediaType(), params)
	if single.Encoding != "" {
		h.Set("Content-Transfer-Encoding", single.Encoding)
	}
	var body io.Reader = bytes.NewReader(raw)
	// Con un juego de caracteres o una codificacion desconocidos, New devuelve la entidad
	// con los bytes sin convertir: se muestran como UTF-8 saneado antes que nada.
	if entity, _ := gomessage.New(h, bytes.NewReader(raw)); entity != nil {
		body = entity.Body
	}
	// Un base64 cortado por el tope de lectura acaba en error: lo leido hasta ahi vale.
	data, _ := io.ReadAll(io.LimitReader(body, maxBytes+1))
	truncated := encodedTruncated || int64(len(data)) > maxBytes
	if int64(len(data)) > maxBytes {
		data = data[:maxBytes]
	}
	return strings.ToValidUTF8(string(data), "�"), truncated
}

func referencesSection() *imaplib.FetchItemBodySection {
	return &imaplib.FetchItemBodySection{Specifier: imaplib.PartSpecifierHeader, HeaderFields: []string{"References"}, Peek: true}
}

func headerSection(buf *imapclient.FetchMessageBuffer) []byte {
	for _, s := range buf.BodySection {
		if s.Section != nil && s.Section.Specifier == imaplib.PartSpecifierHeader && len(s.Section.Part) == 0 {
			return s.Bytes
		}
	}
	return nil
}

func partSection(buf *imapclient.FetchMessageBuffer, path []int) []byte {
	for _, s := range buf.BodySection {
		if s.Section != nil && s.Section.Specifier == imaplib.PartSpecifierNone && equalPath(s.Section.Part, path) {
			return s.Bytes
		}
	}
	return nil
}

// parseReferences lee la cabecera References devuelta por HEADER.FIELDS.
func parseReferences(raw []byte) []string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	th, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return nil
	}
	h := mail.Header{Header: gomessage.Header{Header: th}}
	ids, err := h.MsgIDList("References")
	if err != nil {
		return nil
	}
	if len(ids) > maxReferences {
		ids = ids[len(ids)-maxReferences:]
	}
	return ids
}

func bareMessageID(id string) string {
	return strings.Trim(strings.TrimSpace(id), "<>")
}

func envelopeOf(b *imapclient.FetchMessageBuffer) domain.Envelope {
	e := domain.Envelope{UID: uint32(b.UID), Size: b.RFC822Size, Flags: systemFlags(b.Flags)}
	if env := b.Envelope; env != nil {
		e.From = addressesOf(env.From)
		e.To = addressesOf(env.To)
		e.Cc = addressesOf(env.Cc)
		e.Subject = env.Subject
		e.Date = env.Date
	}
	if e.Date.IsZero() {
		e.Date = b.InternalDate
	}
	if b.BodyStructure != nil {
		e.HasAttachments = hasAttachments(b.BodyStructure)
	}
	return e
}

func addressesOf(list []imaplib.Address) []domain.Address {
	var out []domain.Address
	for i := range list {
		a := list[i]
		if a.IsGroupStart() || a.IsGroupEnd() {
			continue
		}
		if email := a.Addr(); email != "" {
			out = append(out, domain.Address{Name: a.Name, Email: email})
		}
	}
	return out
}

func systemFlags(flags []imaplib.Flag) []domain.Flag {
	var out []domain.Flag
	for _, f := range flags {
		for _, sys := range domain.SystemFlags {
			if strings.EqualFold(string(f), string(sys)) {
				out = append(out, sys)
			}
		}
	}
	return out
}

func hasFlag(flags []imaplib.Flag, want imaplib.Flag) bool {
	for _, f := range flags {
		if strings.EqualFold(string(f), string(want)) {
			return true
		}
	}
	return false
}

func imapFlags(flags []domain.Flag) []imaplib.Flag {
	out := make([]imaplib.Flag, len(flags))
	for i, f := range flags {
		out[i] = imaplib.Flag(f)
	}
	return out
}

func selectable(attrs []imaplib.MailboxAttr) bool {
	for _, a := range attrs {
		if strings.EqualFold(string(a), string(imaplib.MailboxAttrNoSelect)) ||
			strings.EqualFold(string(a), string(imaplib.MailboxAttrNonExistent)) {
			return false
		}
	}
	return true
}

var specialUseRoles = map[string]domain.FolderRole{
	`\sent`:    domain.RoleSent,
	`\drafts`:  domain.RoleDrafts,
	`\trash`:   domain.RoleTrash,
	`\junk`:    domain.RoleJunk,
	`\archive`: domain.RoleArchive,
}

// roleOf usa el atributo SPECIAL-USE cuando el servidor lo anuncia; sin el, el nombre.
// No se mezclan: con SPECIAL-USE una carpeta propia llamada "Trash" no debe competir con
// la papelera que marca el servidor.
func roleOf(d *imaplib.ListData, specialUse bool) domain.FolderRole {
	if strings.EqualFold(d.Mailbox, "INBOX") {
		return domain.RoleInbox
	}
	if !specialUse {
		return domain.RoleByName(d.Mailbox)
	}
	if domain.IsScheduledFolderName(d.Mailbox) {
		return domain.RoleScheduled
	}
	if domain.IsSnoozedFolderName(d.Mailbox) {
		return domain.RoleSnoozed
	}
	for _, a := range d.Attrs {
		if role, ok := specialUseRoles[strings.ToLower(string(a))]; ok {
			return role
		}
	}
	return domain.RoleNone
}

// sortFolders deja primero las especiales en el orden de domain.SpecialRoles y despues el
// resto por nombre.
func sortFolders(folders []domain.Folder) {
	rank := func(f domain.Folder) int {
		for i, role := range domain.SpecialRoles {
			if f.Role == role {
				return i
			}
		}
		return len(domain.SpecialRoles)
	}
	sort.SliceStable(folders, func(i, j int) bool {
		ri, rj := rank(folders[i]), rank(folders[j])
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(folders[i].Name) < strings.ToLower(folders[j].Name)
	})
}
