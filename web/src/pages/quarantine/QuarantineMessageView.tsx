import { mailSecurityApi } from '@/api/mailSecurity';
import { useQuery } from '@/hooks/useQuery';
import { Alert, ErrorState, Skeleton } from '@/design/components';
import { getLocale, t } from '@/i18n';

// Cuanto texto del mensaje se pinta; el resto se omite para no bloquear el navegador con
// un adjunto grande codificado en base64.
export const MESSAGE_PREVIEW_CHARS = 256 * 1024;

export interface QuarantineMessageTextProps {
  source: string;
  limit?: number;
}

/**
 * El mensaje en cuarentena es correo sospechoso: se muestra como texto plano dentro de un
 * <pre> y React lo escapa. Nunca se interpreta su HTML ni se cargan sus recursos.
 */
export function QuarantineMessageText({
  source,
  limit = MESSAGE_PREVIEW_CHARS,
}: QuarantineMessageTextProps) {
  const truncated = source.length > limit;
  const shown = truncated ? source.slice(0, limit) : source;
  const format = new Intl.NumberFormat(getLocale());
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-2)' }}>
      <Alert tone="warning">{t('quarantine.message.asText')}</Alert>
      <pre className="cf-pre cf-pre--tall" data-testid="quarantine-message-source">
        {shown}
      </pre>
      {truncated ? (
        <span className="cf-text-sm cf-text-muted">
          {t('quarantine.message.truncated', {
            shown: format.format(shown.length),
            total: format.format(source.length),
          })}
        </span>
      ) : null}
    </div>
  );
}

export function QuarantineMessage({ id }: { id: string }) {
  const message = useQuery(() => mailSecurityApi.quarantineMessage(id), [id]);
  if (message.error) return <ErrorState error={message.error} onRetry={message.reload} />;
  if (message.data === null) return <Skeleton lines={6} />;
  return <QuarantineMessageText source={message.data} />;
}
