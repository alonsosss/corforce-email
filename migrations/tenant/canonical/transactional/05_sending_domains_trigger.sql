-- Schema: transactional | Service: transactional
--
-- sending_domains es mutable (UpsertSendingDomain la actualiza en cada evento
-- domains.domain.verified|failed|deleted) pero, a diferencia de messages, nunca tuvo el
-- trigger update_updated_at: quedaba solo al cuidado del UPSERT en Go. Se agrega aqui para
-- que la columna quede garantizada por la base, igual que el resto del esquema, sin
-- depender de que todo escritor futuro recuerde fijarla.

DO $$
BEGIN
    CREATE TRIGGER trg_transactional_sending_domains_updated
        BEFORE UPDATE ON transactional.sending_domains
        FOR EACH ROW EXECUTE FUNCTION update_updated_at();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
