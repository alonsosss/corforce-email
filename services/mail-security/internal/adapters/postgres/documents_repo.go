package postgres

import (
	"context"
	"errors"

	"github.com/alonsosss/corforce-email/pkg/db"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/jackc/pgx/v5"
)

// DocumentRepository implementa ports.DocumentStamps sobre mail_security.engine_documents.
// Corre como duena del pool dentro de la transaccion del caso de uso: la tabla no tiene
// empresa y mail_app no la ve.
type DocumentRepository struct {
	pool *db.ContextPool
}

func NewDocumentRepository(pool *db.ContextPool) *DocumentRepository {
	return &DocumentRepository{pool: pool}
}

const documentColumns = `document, content_hash, last_modified`

func scanDocument(row pgx.Row) (domain.DocumentStamp, error) {
	var s domain.DocumentStamp
	err := row.Scan(&s.Document, &s.ContentHash, &s.LastModified)
	s.LastModified = s.LastModified.UTC()
	return s, err
}

func (r *DocumentRepository) LockDocument(ctx context.Context, document string) (*domain.DocumentStamp, error) {
	s, err := scanDocument(r.pool.QueryRow(ctx,
		`SELECT `+documentColumns+` FROM mail_security.engine_documents WHERE document = $1 FOR UPDATE`, document))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// SaveDocument solo adelanta la marca. Si otra replica dio de alta el documento a la vez
// con el mismo contenido, su fila se queda como esta y se devuelve.
func (r *DocumentRepository) SaveDocument(ctx context.Context, s domain.DocumentStamp) (domain.DocumentStamp, error) {
	saved, err := scanDocument(r.pool.QueryRow(ctx, `
		INSERT INTO mail_security.engine_documents (document, content_hash, last_modified)
		VALUES ($1, $2, $3)
		ON CONFLICT (document) DO UPDATE
		   SET content_hash = EXCLUDED.content_hash,
		       last_modified = GREATEST(EXCLUDED.last_modified, mail_security.engine_documents.last_modified + interval '1 second')
		 WHERE mail_security.engine_documents.content_hash <> EXCLUDED.content_hash
		RETURNING `+documentColumns, s.Document, s.ContentHash, s.LastModified))
	if errors.Is(err, pgx.ErrNoRows) {
		return scanDocument(r.pool.QueryRow(ctx,
			`SELECT `+documentColumns+` FROM mail_security.engine_documents WHERE document = $1`, s.Document))
	}
	return saved, err
}
