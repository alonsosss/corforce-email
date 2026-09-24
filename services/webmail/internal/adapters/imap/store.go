// Package imap habla IMAP con el Dovecot de la celda.
//
// Cada peticion abre una conexion, inicia sesion como usuario maestro en nombre del
// buzon (usuario*maestro, auth_master_user_separator de Dovecot) y la cierra al terminar.
// La contrasena del buzon nunca se guarda: la comprobo mail-auth al abrir la sesion del
// webmail y la revocacion por eventos cierra la puerta si cambia.
package imap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	imaplib "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomessage "github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	"go.uber.org/zap"
)

// Modos de TLS con Dovecot. TLSNone solo existe para desarrollo y pruebas: Dovecot
// rechaza LOGIN en claro fuera de loopback (disable_plaintext_auth).
const (
	TLSImplicit = "implicit"
	TLSStartTLS = "starttls"
	TLSNone     = "none"

	masterSeparator = "*"
	maxFolders      = 1000
	logoutTimeout   = 2 * time.Second
)

type Config struct {
	Addr           string
	TLSMode        string
	TLSConfig      *tls.Config
	MasterUser     string
	MasterPassword string
	DialTimeout    time.Duration
}

// Store implementa ports.MailStore.
type Store struct {
	cfg         Config
	logger      *zap.Logger
	wordDecoder *mime.WordDecoder
}

func NewStore(cfg Config, logger *zap.Logger) (*Store, error) {
	if cfg.Addr == "" || cfg.MasterUser == "" || cfg.MasterPassword == "" {
		return nil, errors.New("imap: faltan la direccion o la credencial maestra")
	}
	switch cfg.TLSMode {
	case TLSImplicit, TLSStartTLS:
		if cfg.TLSConfig == nil || cfg.TLSConfig.ServerName == "" {
			return nil, errors.New("imap: falta el nombre de servidor TLS")
		}
	case TLSNone:
	default:
		return nil, fmt.Errorf("imap: modo TLS invalido %q (implicit, starttls o none)", cfg.TLSMode)
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	return &Store{cfg: cfg, logger: logger, wordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader}}, nil
}

// Open inicia sesion en nombre del buzon. El nombre ya viene normalizado de la sesion;
// se vuelve a rechazar el separador maestro aqui porque es lo que decide a que buzon se
// entra.
func (s *Store) Open(ctx context.Context, username string) (ports.Mailbox, error) {
	c, err := s.dial(ctx, username, nil)
	if err != nil {
		return nil, err
	}
	return &mailbox{c: c, logger: s.logger}, nil
}

// dial abre la conexion y entra en nombre del buzon. unilateral, si no es nil, recibe lo que Dovecot avisa
// sin que se le pida (mensajes nuevos, banderas), que es lo que usa la vigilancia de la bandeja.
func (s *Store) dial(ctx context.Context, username string, unilateral *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	if username == "" || strings.ContainsAny(username, masterSeparator+"\r\n\x00 \"") {
		return nil, fmt.Errorf("%w: nombre de buzon invalido", domain.ErrUnavailable)
	}
	d := net.Dialer{Timeout: s.cfg.DialTimeout}
	conn, err := d.DialContext(ctx, "tcp", s.cfg.Addr)
	if err != nil {
		return nil, unavailable("conectar con IMAP", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	opts := &imapclient.Options{TLSConfig: s.cfg.TLSConfig, WordDecoder: s.wordDecoder, UnilateralDataHandler: unilateral}
	var c *imapclient.Client
	switch s.cfg.TLSMode {
	case TLSImplicit:
		tconn := tls.Client(conn, s.cfg.TLSConfig.Clone())
		if err := tconn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, unavailable("TLS con IMAP", err)
		}
		c = imapclient.New(tconn, opts)
	case TLSStartTLS:
		c, err = imapclient.NewStartTLS(conn, opts)
		if err != nil {
			return nil, unavailable("STARTTLS con IMAP", err)
		}
	default:
		c = imapclient.New(conn, opts)
	}
	if err := c.WaitGreeting(); err != nil {
		_ = c.Close()
		return nil, unavailable("saludo IMAP", err)
	}
	if err := c.Login(username+masterSeparator+s.cfg.MasterUser, s.cfg.MasterPassword).Wait(); err != nil {
		_ = c.Close()
		s.logger.Error("webmail: Dovecot rechazo el inicio en nombre del buzon", zap.String("username", username), zap.Error(err))
		return nil, unavailable("inicio IMAP", err)
	}
	return c, nil
}

// mailbox implementa ports.Mailbox sobre una conexion autenticada. No es concurrente:
// cada peticion HTTP abre la suya.
type mailbox struct {
	c           *imapclient.Client
	logger      *zap.Logger
	selected    string
	writable    bool
	uidValidity uint32
}

// watch cierra la conexion si el contexto se cancela: imapclient no acepta contexto por
// comando y un servidor que no responde colgaria la peticion.
func (m *mailbox) watch(ctx context.Context) func() {
	stop := context.AfterFunc(ctx, func() { _ = m.c.Close() })
	return func() { stop() }
}

func (m *mailbox) Close() error {
	done := make(chan struct{})
	go func() {
		_ = m.c.Logout().Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(logoutTimeout):
	}
	_ = m.c.Close()
	return nil
}

func (m *mailbox) selectFolder(name string, writable bool) error {
	if m.selected == name && (m.writable || !writable) {
		return nil
	}
	data, err := m.c.Select(name, &imaplib.SelectOptions{ReadOnly: !writable}).Wait()
	if err != nil {
		m.selected = ""
		return mapError(err, domain.ErrFolderNotFound)
	}
	m.selected, m.writable, m.uidValidity = name, writable, data.UIDValidity
	return nil
}

func (m *mailbox) Folders(ctx context.Context, withCounts bool) ([]domain.Folder, error) {
	defer m.watch(ctx)()
	caps := m.c.Caps()
	specialUse := caps.Has(imaplib.CapSpecialUse) || caps.Has(imaplib.CapIMAP4rev2)
	listStatus := withCounts && (caps.Has(imaplib.CapListStatus) || caps.Has(imaplib.CapIMAP4rev2))
	opts := &imaplib.ListOptions{ReturnSpecialUse: specialUse}
	if listStatus {
		opts.ReturnStatus = &imaplib.StatusOptions{NumMessages: true, NumUnseen: true}
	}
	data, err := m.c.List("", "*", opts).Collect()
	if err != nil {
		return nil, mapError(err, nil)
	}
	if len(data) > maxFolders {
		m.logger.Warn("webmail: buzon con demasiadas carpetas; se listan las primeras", zap.Int("folders", len(data)))
		data = data[:maxFolders]
	}
	out := make([]domain.Folder, 0, len(data))
	for _, d := range data {
		f := domain.Folder{Name: d.Mailbox, Selectable: selectable(d.Attrs), Role: roleOf(d, specialUse)}
		if d.Delim != 0 {
			f.Delimiter = string(d.Delim)
		}
		switch {
		case d.Status != nil:
			f.Total, f.Unread = deref(d.Status.NumMessages), deref(d.Status.NumUnseen)
		case withCounts && f.Selectable:
			st, err := m.c.Status(d.Mailbox, &imaplib.StatusOptions{NumMessages: true, NumUnseen: true}).Wait()
			if err == nil {
				f.Total, f.Unread = deref(st.NumMessages), deref(st.NumUnseen)
			}
		}
		out = append(out, f)
	}
	sortFolders(out)
	return out, nil
}

func (m *mailbox) Quota(ctx context.Context) (*domain.Quota, error) {
	defer m.watch(ctx)()
	if !m.c.Caps().Has(imaplib.CapQuota) {
		return nil, nil
	}
	roots, err := m.c.GetQuotaRoot("INBOX").Wait()
	if err != nil {
		return nil, mapError(err, nil)
	}
	for _, r := range roots {
		// STORAGE va en unidades de 1024 octetos (RFC 9208).
		if res, ok := r.Resources[imaplib.QuotaResourceStorage]; ok {
			return &domain.Quota{UsedBytes: res.Usage * 1024, LimitBytes: res.Limit * 1024}, nil
		}
	}
	return nil, nil
}

func (m *mailbox) List(ctx context.Context, folder string, q domain.ListQuery) (domain.MessagePage, error) {
	defer m.watch(ctx)()
	if err := m.selectFolder(folder, false); err != nil {
		return domain.MessagePage{}, err
	}
	uids, err := m.sortedUIDs(searchCriteria(q))
	if err != nil {
		return domain.MessagePage{}, err
	}
	capped := false
	if q.Filter.HasAttachments {
		if uids, capped, err = m.withAttachments(uids); err != nil {
			return domain.MessagePage{}, err
		}
	}
	total := len(uids)
	start, end := q.Window(total)
	page := uids[start:end]
	if len(page) == 0 {
		return domain.MessagePage{Total: total, Capped: capped}, nil
	}
	bufs, err := m.c.Fetch(imaplib.UIDSetNum(page...), &imaplib.FetchOptions{
		UID: true, Envelope: true, Flags: true, RFC822Size: true, InternalDate: true,
		BodyStructure: &imaplib.FetchItemBodyStructure{Extended: true},
	}).Collect()
	if err != nil {
		return domain.MessagePage{}, mapError(err, nil)
	}
	byUID := make(map[imaplib.UID]*imapclient.FetchMessageBuffer, len(bufs))
	for _, b := range bufs {
		byUID[b.UID] = b
	}
	items := make([]domain.Envelope, 0, len(page))
	for _, uid := range page {
		if b := byUID[uid]; b != nil {
			items = append(items, envelopeOf(b))
		}
	}
	return domain.MessagePage{Items: items, Total: total, Capped: capped}, nil
}

// searchCriteria traduce la busqueda a IMAP SEARCH. Los textos los cita la libreria y Dovecot los
// busca como subcadena sin distinguir mayusculas; las fechas son las de la cabecera Date, las
// mismas por las que se ordena el listado.
func searchCriteria(q domain.ListQuery) *imaplib.SearchCriteria {
	c := &imaplib.SearchCriteria{}
	if q.Search != "" {
		c.Text = []string{q.Search}
	}
	f := q.Filter
	for _, h := range []struct{ key, value string }{{"From", f.From}, {"To", f.To}, {"Subject", f.Subject}} {
		if h.value != "" {
			c.Header = append(c.Header, imaplib.SearchCriteriaHeaderField{Key: h.key, Value: h.value})
		}
	}
	c.SentSince, c.SentBefore = f.Since, f.Before
	if f.Unread {
		c.NotFlag = append(c.NotFlag, imaplib.FlagSeen)
	}
	if f.Flagged {
		c.Flag = append(c.Flag, imaplib.FlagFlagged)
	}
	if f.HasAttachments {
		// Descarta en el servidor los mensajes de una sola parte de texto, que no pueden llevar
		// adjuntos; el resto se decide con BODYSTRUCTURE (withAttachments).
		c.Not = append(c.Not, imaplib.SearchCriteria{Header: []imaplib.SearchCriteriaHeaderField{{Key: "Content-Type", Value: "text/"}}})
	}
	return c
}

// Filtro por adjuntos: IMAP SEARCH no sabe si un mensaje tiene adjuntos, asi que se decide con la
// misma regla que marca has_attachments en el listado (hasAttachments sobre BODYSTRUCTURE), leida
// por lotes y solo sobre los mensajes mas recientes que casan con el resto de criterios. Si hay
// mas, el total se declara acotado (capped) en vez de recorrer la carpeta entera.
const (
	attachmentScanLimit = 2000
	attachmentScanBatch = 500
)

func (m *mailbox) withAttachments(uids []imaplib.UID) ([]imaplib.UID, bool, error) {
	capped := len(uids) > attachmentScanLimit
	if capped {
		uids = uids[:attachmentScanLimit]
	}
	with := make(map[imaplib.UID]bool, len(uids))
	for start := 0; start < len(uids); start += attachmentScanBatch {
		chunk := uids[start:min(start+attachmentScanBatch, len(uids))]
		bufs, err := m.c.Fetch(imaplib.UIDSetNum(chunk...), &imaplib.FetchOptions{
			UID: true, BodyStructure: &imaplib.FetchItemBodyStructure{Extended: true},
		}).Collect()
		if err != nil {
			return nil, false, mapError(err, nil)
		}
		for _, b := range bufs {
			if b.BodyStructure != nil && hasAttachments(b.BodyStructure) {
				with[b.UID] = true
			}
		}
	}
	out := make([]imaplib.UID, 0, len(with))
	for _, uid := range uids {
		if with[uid] {
			out = append(out, uid)
		}
	}
	return out, capped, nil
}

// sortedUIDs devuelve los UIDs que casan con la busqueda, del mas reciente al mas
// antiguo. Con SORT (Dovecot) ordena el servidor por fecha; sin el, por UID, que sigue
// el orden de llegada.
func (m *mailbox) sortedUIDs(criteria *imaplib.SearchCriteria) ([]imaplib.UID, error) {
	if m.c.Caps().Has(imaplib.CapSort) {
		nums, err := m.c.UIDSort(&imapclient.SortOptions{
			SearchCriteria: criteria,
			SortCriteria:   []imapclient.SortCriterion{{Key: imapclient.SortKeyDate, Reverse: true}},
		}).Wait()
		if err != nil {
			return nil, mapError(err, nil)
		}
		out := make([]imaplib.UID, len(nums))
		for i, n := range nums {
			out[i] = imaplib.UID(n)
		}
		return out, nil
	}
	data, err := m.c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, mapError(err, nil)
	}
	uids := data.AllUIDs()
	sort.Slice(uids, func(i, j int) bool { return uids[i] > uids[j] })
	return uids, nil
}

func (m *mailbox) Read(ctx context.Context, folder string, uid uint32, opts domain.ReadOptions) (*domain.RawMessage, error) {
	defer m.watch(ctx)()
	if err := m.selectFolder(folder, opts.MarkSeen); err != nil {
		return nil, err
	}
	set := imaplib.UIDSetNum(imaplib.UID(uid))
	bufs, err := m.c.Fetch(set, &imaplib.FetchOptions{
		UID: true, Envelope: true, Flags: true, RFC822Size: true, InternalDate: true,
		BodyStructure: &imaplib.FetchItemBodyStructure{Extended: true},
		BodySection:   []*imaplib.FetchItemBodySection{referencesSection()},
	}).Collect()
	if err != nil {
		return nil, mapError(err, nil)
	}
	if len(bufs) == 0 || bufs[0].BodyStructure == nil {
		return nil, domain.ErrMessageNotFound
	}
	buf := bufs[0]
	msg := &domain.RawMessage{Envelope: envelopeOf(buf)}
	if env := buf.Envelope; env != nil {
		msg.Bcc = addressesOf(env.Bcc)
		msg.ReplyTo = addressesOf(env.ReplyTo)
		msg.MessageID = bareMessageID(env.MessageID)
		for _, id := range env.InReplyTo {
			msg.InReplyTo = append(msg.InReplyTo, bareMessageID(id))
		}
	}
	msg.References = parseReferences(headerSection(buf))

	plan := planBody(buf.BodyStructure)
	msg.Parts = plan.parts
	if err := m.readBodies(set, plan, opts.MaxBodyBytes, msg); err != nil {
		return nil, err
	}

	if opts.MarkSeen && !hasFlag(buf.Flags, imaplib.FlagSeen) {
		err := m.c.Store(set, &imaplib.StoreFlags{Op: imaplib.StoreFlagsAdd, Silent: true, Flags: []imaplib.Flag{imaplib.FlagSeen}}, nil).Close()
		if err != nil {
			m.logger.Warn("webmail: no se pudo marcar el mensaje como leido", zap.Error(err))
		} else {
			msg.Flags = append(msg.Flags, domain.FlagSeen)
		}
	}
	return msg, nil
}

// readBodies trae el texto y el HTML elegidos en una sola orden, acotados: se piden como
// mucho el doble de bytes codificados que el tope decodificado.
func (m *mailbox) readBodies(set imaplib.UIDSet, plan bodyPlan, maxBytes int64, msg *domain.RawMessage) error {
	leaves := []*leaf{plan.text, plan.html}
	var sections []*imaplib.FetchItemBodySection
	encodedLimit := maxBytes*2 + 1024
	for _, l := range leaves {
		if l != nil {
			sections = append(sections, &imaplib.FetchItemBodySection{
				Part: l.path, Peek: true, Partial: &imaplib.SectionPartial{Offset: 0, Size: encodedLimit},
			})
		}
	}
	if len(sections) == 0 {
		return nil
	}
	bufs, err := m.c.Fetch(set, &imaplib.FetchOptions{UID: true, BodySection: sections}).Collect()
	if err != nil {
		return mapError(err, nil)
	}
	if len(bufs) == 0 {
		return domain.ErrMessageNotFound
	}
	if plan.text != nil {
		raw := partSection(bufs[0], plan.text.path)
		msg.Text, msg.TextTruncated = decodeText(raw, plan.text.part, maxBytes, int64(plan.text.part.Size) > encodedLimit)
	}
	if plan.html != nil {
		raw := partSection(bufs[0], plan.html.path)
		msg.HTML, msg.HTMLTruncated = decodeText(raw, plan.html.part, maxBytes, int64(plan.html.part.Size) > encodedLimit)
	}
	return nil
}

func (m *mailbox) OpenPart(ctx context.Context, folder string, uid uint32, partID string, maxBytes int64) (domain.Part, io.ReadCloser, error) {
	stop := m.watch(ctx)
	part, body, err := m.openPart(folder, uid, partID, maxBytes)
	if err != nil {
		stop()
		return domain.Part{}, nil, err
	}
	body.release = stop
	return part, body, nil
}

func (m *mailbox) openPart(folder string, uid uint32, partID string, maxBytes int64) (domain.Part, *partReader, error) {
	path, err := domain.ParsePartID(partID)
	if err != nil {
		return domain.Part{}, nil, err
	}
	if err := m.selectFolder(folder, false); err != nil {
		return domain.Part{}, nil, err
	}
	set := imaplib.UIDSetNum(imaplib.UID(uid))
	bufs, err := m.c.Fetch(set, &imaplib.FetchOptions{UID: true, BodyStructure: &imaplib.FetchItemBodyStructure{Extended: true}}).Collect()
	if err != nil {
		return domain.Part{}, nil, mapError(err, nil)
	}
	if len(bufs) == 0 || bufs[0].BodyStructure == nil {
		return domain.Part{}, nil, domain.ErrMessageNotFound
	}
	single := findLeaf(bufs[0].BodyStructure, path)
	if single == nil {
		return domain.Part{}, nil, domain.ErrPartNotFound
	}
	part := partOf(path, single)
	if part.Size > maxBytes {
		return domain.Part{}, nil, domain.ErrPartTooLarge
	}

	cmd := m.c.Fetch(set, &imaplib.FetchOptions{UID: true, BodySection: []*imaplib.FetchItemBodySection{{Part: path, Peek: true}}})
	literal := firstLiteral(cmd)
	if literal == nil {
		_ = cmd.Close()
		return domain.Part{}, nil, domain.ErrPartNotFound
	}
	// Se decodifica la codificacion de transferencia, no el juego de caracteres: el
	// adjunto se entrega con sus bytes originales.
	h := gomessage.Header{}
	h.SetContentType(single.MediaType(), nil)
	if single.Encoding != "" {
		h.Set("Content-Transfer-Encoding", single.Encoding)
	}
	var body io.Reader = literal
	if entity, err := gomessage.New(h, literal); entity != nil && err == nil {
		body = entity.Body
	}
	return part, &partReader{r: body, remaining: maxBytes, finish: cmd.Close}, nil
}

// partReader limita la parte a maxBytes y libera la orden FETCH al cerrarse.
type partReader struct {
	r         io.Reader
	remaining int64
	finish    func() error
	release   func()
	closed    bool
	// tooLarge es el error al pasar del tope; sin fijar, domain.ErrPartTooLarge.
	tooLarge error
}

func (p *partReader) Read(b []byte) (int, error) {
	if p.remaining <= 0 {
		var probe [1]byte
		if n, _ := p.r.Read(probe[:]); n > 0 {
			if p.tooLarge != nil {
				return 0, p.tooLarge
			}
			return 0, domain.ErrPartTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(b)) > p.remaining {
		b = b[:p.remaining]
	}
	n, err := p.r.Read(b)
	p.remaining -= int64(n)
	return n, err
}

func (p *partReader) Close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	err := p.finish()
	if p.release != nil {
		p.release()
	}
	return err
}

func (m *mailbox) ReplyReference(ctx context.Context, folder string, uid uint32) (domain.ReplyReference, error) {
	defer m.watch(ctx)()
	if err := m.selectFolder(folder, false); err != nil {
		return domain.ReplyReference{}, err
	}
	bufs, err := m.c.Fetch(imaplib.UIDSetNum(imaplib.UID(uid)), &imaplib.FetchOptions{
		UID: true, Envelope: true, BodySection: []*imaplib.FetchItemBodySection{referencesSection()},
	}).Collect()
	if err != nil {
		return domain.ReplyReference{}, mapError(err, nil)
	}
	if len(bufs) == 0 {
		return domain.ReplyReference{}, domain.ErrMessageNotFound
	}
	ref := domain.ReplyReference{References: parseReferences(headerSection(bufs[0]))}
	if env := bufs[0].Envelope; env != nil {
		ref.MessageID = bareMessageID(env.MessageID)
	}
	return ref, nil
}

func (m *mailbox) SetFlags(ctx context.Context, folder string, uids []uint32, change domain.FlagChange) (int, error) {
	defer m.watch(ctx)()
	if err := m.selectFolder(folder, true); err != nil {
		return 0, err
	}
	set, n, err := m.existing(uids)
	if err != nil || n == 0 {
		return 0, err
	}
	for _, op := range []struct {
		kind  imaplib.StoreFlagsOp
		flags []domain.Flag
	}{{imaplib.StoreFlagsAdd, change.Add}, {imaplib.StoreFlagsDel, change.Remove}} {
		if len(op.flags) == 0 {
			continue
		}
		if err := m.c.Store(set, &imaplib.StoreFlags{Op: op.kind, Silent: true, Flags: imapFlags(op.flags)}, nil).Close(); err != nil {
			return 0, mapError(err, nil)
		}
	}
	return n, nil
}

func (m *mailbox) Move(ctx context.Context, folder string, uids []uint32, dest string) (int, error) {
	defer m.watch(ctx)()
	if err := m.selectFolder(folder, true); err != nil {
		return 0, err
	}
	set, n, err := m.existing(uids)
	if err != nil || n == 0 {
		return 0, err
	}
	if _, err := m.c.Move(set, dest).Wait(); err != nil {
		return 0, mapError(err, domain.ErrFolderNotFound)
	}
	return n, nil
}

// Expunge borra los mensajes indicados. Exige UID EXPUNGE (UIDPLUS): un EXPUNGE sin UID
// borraria tambien cualquier otro mensaje que otro cliente hubiera marcado \Deleted.
func (m *mailbox) Expunge(ctx context.Context, folder string, uids []uint32) (int, error) {
	defer m.watch(ctx)()
	if err := m.requireUIDPlus(); err != nil {
		return 0, err
	}
	if err := m.selectFolder(folder, true); err != nil {
		return 0, err
	}
	set, n, err := m.existing(uids)
	if err != nil || n == 0 {
		return 0, err
	}
	return n, m.expungeSet(set)
}

// Empty borra todos los mensajes de la carpeta con UID EXPUNGE sobre los UIDs que habia al
// empezar: un mensaje que llegue mientras tanto no se borra.
func (m *mailbox) Empty(ctx context.Context, folder string) (int, error) {
	defer m.watch(ctx)()
	if err := m.requireUIDPlus(); err != nil {
		return 0, err
	}
	if err := m.selectFolder(folder, true); err != nil {
		return 0, err
	}
	data, err := m.c.UIDSearch(&imaplib.SearchCriteria{}, nil).Wait()
	if err != nil {
		return 0, mapError(err, nil)
	}
	uids := data.AllUIDs()
	if len(uids) == 0 {
		return 0, nil
	}
	return len(uids), m.expungeSet(imaplib.UIDSetNum(uids...))
}

func (m *mailbox) expungeSet(set imaplib.UIDSet) error {
	store := &imaplib.StoreFlags{Op: imaplib.StoreFlagsAdd, Silent: true, Flags: []imaplib.Flag{imaplib.FlagDeleted}}
	if err := m.c.Store(set, store, nil).Close(); err != nil {
		return mapError(err, nil)
	}
	if err := m.c.UIDExpunge(set).Close(); err != nil {
		return mapError(err, nil)
	}
	return nil
}

func (m *mailbox) requireUIDPlus() error {
	caps := m.c.Caps()
	if !caps.Has(imaplib.CapUIDPlus) && !caps.Has(imaplib.CapIMAP4rev2) {
		return fmt.Errorf("%w: el servidor IMAP no admite UID EXPUNGE", domain.ErrUnavailable)
	}
	return nil
}

func (m *mailbox) Append(ctx context.Context, folder string, raw []byte, flags []domain.Flag, date time.Time) (domain.AppendedMessage, error) {
	defer m.watch(ctx)()
	cmd := m.c.Append(folder, int64(len(raw)), &imaplib.AppendOptions{Flags: imapFlags(flags), Time: date})
	if _, err := cmd.Write(raw); err != nil {
		_ = cmd.Close()
		return domain.AppendedMessage{}, unavailable("APPEND", err)
	}
	if err := cmd.Close(); err != nil {
		return domain.AppendedMessage{}, unavailable("APPEND", err)
	}
	data, err := cmd.Wait()
	if err != nil {
		return domain.AppendedMessage{}, mapError(err, domain.ErrFolderNotFound)
	}
	return domain.AppendedMessage{UID: uint32(data.UID), UIDValidity: data.UIDValidity}, nil
}

func (m *mailbox) Stat(ctx context.Context, folder string, uid uint32) (domain.StoredMessage, error) {
	defer m.watch(ctx)()
	return m.stat(folder, uid)
}

func (m *mailbox) stat(folder string, uid uint32) (domain.StoredMessage, error) {
	if err := m.selectFolder(folder, false); err != nil {
		return domain.StoredMessage{}, err
	}
	bufs, err := m.c.Fetch(imaplib.UIDSetNum(imaplib.UID(uid)), &imaplib.FetchOptions{UID: true, Envelope: true, RFC822Size: true}).Collect()
	if err != nil {
		return domain.StoredMessage{}, mapError(err, nil)
	}
	if len(bufs) == 0 {
		return domain.StoredMessage{}, domain.ErrMessageNotFound
	}
	msg := domain.StoredMessage{UIDValidity: m.uidValidity, UID: uint32(bufs[0].UID), Size: bufs[0].RFC822Size}
	if env := bufs[0].Envelope; env != nil {
		msg.MessageID = bareMessageID(env.MessageID)
	}
	return msg, nil
}

// OpenRaw entrega el mensaje tal como esta guardado (BODY.PEEK[]: no lo marca como leido).
func (m *mailbox) OpenRaw(ctx context.Context, folder string, uid uint32, maxBytes int64) (domain.StoredMessage, io.ReadCloser, error) {
	stop := m.watch(ctx)
	msg, err := m.stat(folder, uid)
	if err == nil && msg.Size > maxBytes {
		err = domain.ErrMessageTooLarge
	}
	if err != nil {
		stop()
		return domain.StoredMessage{}, nil, err
	}
	cmd := m.c.Fetch(imaplib.UIDSetNum(imaplib.UID(uid)), &imaplib.FetchOptions{UID: true, BodySection: []*imaplib.FetchItemBodySection{{Peek: true}}})
	literal := firstLiteral(cmd)
	if literal == nil {
		_ = cmd.Close()
		stop()
		return domain.StoredMessage{}, nil, domain.ErrMessageNotFound
	}
	return msg, &partReader{r: literal, remaining: maxBytes, finish: cmd.Close, release: stop, tooLarge: domain.ErrMessageTooLarge}, nil
}

// firstLiteral devuelve la primera seccion de cuerpo de la respuesta FETCH, o nil.
func firstLiteral(cmd *imapclient.FetchCommand) imaplib.LiteralReader {
	msg := cmd.Next()
	if msg == nil {
		return nil
	}
	for item := msg.Next(); item != nil; item = msg.Next() {
		if section, ok := item.(imapclient.FetchItemDataBodySection); ok {
			return section.Literal
		}
	}
	return nil
}

func (m *mailbox) FindByMessageID(ctx context.Context, folder, messageID string) (uint32, error) {
	defer m.watch(ctx)()
	if err := m.selectFolder(folder, false); err != nil {
		return 0, err
	}
	criteria := &imaplib.SearchCriteria{Header: []imaplib.SearchCriteriaHeaderField{{Key: "Message-ID", Value: "<" + messageID + ">"}}}
	data, err := m.c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return 0, mapError(err, nil)
	}
	var found uint32
	for _, uid := range data.AllUIDs() {
		found = max(found, uint32(uid))
	}
	return found, nil
}

func (m *mailbox) CreateFolder(ctx context.Context, name string) error {
	defer m.watch(ctx)()
	if err := m.c.Create(name, nil).Wait(); err != nil {
		return folderError(err)
	}
	m.subscribe(name)
	return nil
}

// subscribe suscribe la carpeta para que los demas clientes IMAP la muestren. Un fallo no deshace
// la carpeta: se registra. SUBSCRIBE se invoca como valor de metodo porque el analizador de
// contratos de eventos (ops/scaffold/eventcontracts) lee toda llamada .Subscribe(...) como una
// suscripcion de NATS.
func (m *mailbox) subscribe(name string) {
	imapSubscribe := m.c.Subscribe
	if err := imapSubscribe(name).Wait(); err != nil {
		m.logger.Warn("webmail: carpeta sin suscribir", zap.String("folder", name), zap.Error(err))
	}
}

func (m *mailbox) RenameFolder(ctx context.Context, name, newName string) error {
	defer m.watch(ctx)()
	m.selected = ""
	if err := m.c.Rename(name, newName, nil).Wait(); err != nil {
		return folderError(err)
	}
	m.subscribe(newName)
	_ = m.c.Unsubscribe(name).Wait()
	return nil
}

func (m *mailbox) DeleteFolder(ctx context.Context, name string) error {
	defer m.watch(ctx)()
	m.selected = ""
	if err := m.c.Delete(name).Wait(); err != nil {
		return mapError(err, domain.ErrFolderNotFound)
	}
	_ = m.c.Unsubscribe(name).Wait()
	return nil
}

// existing se queda con los UIDs que siguen en la carpeta: STORE y MOVE sobre un UID
// inexistente responden OK sin hacer nada y el cliente creeria que la operacion se aplico.
func (m *mailbox) existing(uids []uint32) (imaplib.UIDSet, int, error) {
	if len(uids) == 0 {
		return nil, 0, nil
	}
	req := make([]imaplib.UID, len(uids))
	for i, u := range uids {
		req[i] = imaplib.UID(u)
	}
	bufs, err := m.c.Fetch(imaplib.UIDSetNum(req...), &imaplib.FetchOptions{UID: true}).Collect()
	if err != nil {
		return nil, 0, mapError(err, nil)
	}
	found := make([]imaplib.UID, 0, len(bufs))
	for _, b := range bufs {
		found = append(found, b.UID)
	}
	if len(found) == 0 {
		return nil, 0, nil
	}
	return imaplib.UIDSetNum(found...), len(found), nil
}

// folderError traduce el rechazo de CREATE o RENAME: un nombre que ya existe, una carpeta que no
// existe o un nombre que el servidor no admite (CANNOT: demasiado largo o con caracteres que su
// almacen no guarda) son del usuario; lo demas es indisponibilidad.
func folderError(err error) error {
	var ie *imaplib.Error
	if errors.As(err, &ie) && ie.Code == imaplib.ResponseCodeCannot {
		return domain.NewValidationError("name", "el servidor de correo no admite ese nombre de carpeta")
	}
	return mapError(err, nil)
}

// mapError traduce una respuesta IMAP. onNo es el error de dominio de un NO sin codigo
// reconocible (una carpeta que no se puede seleccionar, por ejemplo).
func mapError(err error, onNo error) error {
	var ie *imaplib.Error
	if errors.As(err, &ie) {
		switch ie.Code {
		case imaplib.ResponseCodeNonExistent, imaplib.ResponseCodeTryCreate:
			return domain.ErrFolderNotFound
		case imaplib.ResponseCodeOverQuota:
			return domain.ErrQuotaExceeded
		case imaplib.ResponseCodeAlreadyExists:
			return domain.ErrFolderExists
		}
		if ie.Type == imaplib.StatusResponseTypeNo && onNo != nil {
			return onNo
		}
	}
	return unavailable("IMAP", err)
}

func unavailable(op string, err error) error {
	return fmt.Errorf("%w: %s: %v", domain.ErrUnavailable, op, err)
}

func deref(v *uint32) uint32 {
	if v == nil {
		return 0
	}
	return *v
}
