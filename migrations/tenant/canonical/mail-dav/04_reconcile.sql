-- Schema: mail_dav | Service: mail-dav
--
-- Conciliacion de buzones borrados (docs/adr/0004). Las politicas de fila de mail_dav dejan ver a cada sesion
-- solo el buzon de la peticion, asi que el servicio no puede enumerar de que buzones guarda datos. Esta
-- funcion es esa enumeracion, y nada mas: devuelve solo ids de buzon (sin contenido), de la empresa que se le
-- pide, con datos cuyo elemento mas antiguo es anterior a un instante (la ventana de gracia con la que el
-- barrido no toca a un buzon recien creado), en orden y por paginas.
--
-- SECURITY DEFINER porque tiene que saltarse la politica de fila que impide el listado; por eso fija el
-- search_path, filtra por empresa en la propia consulta, tiene un tope de filas y solo la ejecuta el rol de
-- servicio. No escribe ni borra: el borrado sigue siendo por el camino de siempre (buzon, empresa y politicas
-- de fila).
--
-- Idempotente y aditiva.

CREATE OR REPLACE FUNCTION mail_dav.stale_mailbox_ids(p_tenant uuid, p_before timestamptz, p_after uuid, p_limit integer)
RETURNS SETOF uuid
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, mail_dav
AS $$
    SELECT d.mailbox_id
      FROM (
        SELECT mailbox_id, created_at FROM mail_dav.addressbooks WHERE tenant_id = p_tenant AND mailbox_id > p_after
        UNION ALL
        SELECT mailbox_id, created_at FROM mail_dav.calendars WHERE tenant_id = p_tenant AND mailbox_id > p_after
      ) d
     GROUP BY d.mailbox_id
    HAVING min(d.created_at) < p_before
     ORDER BY d.mailbox_id
     LIMIT least(greatest(p_limit, 1), 1000)
$$;

REVOKE ALL ON FUNCTION mail_dav.stale_mailbox_ids(uuid, timestamptz, uuid, integer) FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mail_dav_service') THEN
        GRANT EXECUTE ON FUNCTION mail_dav.stale_mailbox_ids(uuid, timestamptz, uuid, integer) TO mail_dav_service;
    END IF;
END $$;
