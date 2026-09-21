-- Schema: audit | Service: audit
-- Anclas de la cabeza de las cadenas de hash (docs/adr/0006).
--
-- Borrar las ultimas filas de una cadena no deja discontinuidad: lo que queda sigue enlazando.
-- El servicio registra periodicamente aqui la cabeza de cada cadena (posicion y hash) y la
-- publica como evento audit.chain.anchored; el verificador comprueba que la cadena actual
-- sigue conteniendo cada ancla. Una cabeza mas corta que un ancla, o un hash distinto en su
-- posicion, es una rotura.
--
-- Solo anadir: el rol del servicio inserta y lee, nunca modifica ni borra. Un ancla dentro de
-- la MISMA base solo protege contra quien no pueda escribirla tambien: la proteccion completa
-- exige sacarla a un sistema que ese atacante no controle (ADR 0006, "Ancla externa").
--
-- Idempotente y aditiva. Corre despues de 04, que da los permisos amplios.

CREATE TABLE IF NOT EXISTS audit.chain_anchors (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id uuid NOT NULL,
    chain text NOT NULL,
    head_seq bigint NOT NULL,
    head_hash text NOT NULL,
    hash_version smallint NOT NULL,
    anchored_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chain_anchors_chain_check CHECK (chain IN ('audit_logs', 'security_events'))
);

-- Una ancla por posicion y cadena: dos barridos a la vez no duplican la fila, y la primera que
-- se escribe es la que vale.
CREATE UNIQUE INDEX IF NOT EXISTS uq_chain_anchors_chain_seq ON audit.chain_anchors (chain, head_seq);

REVOKE UPDATE, DELETE, TRUNCATE ON audit.chain_anchors FROM audit_service;
GRANT SELECT, INSERT ON audit.chain_anchors TO audit_service;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA audit TO audit_service;
