import { Alert, Card, CopyButton, PageHeader } from '@/design/components';
import { t, type MessageKey } from '@/i18n';

/**
 * Pase de desarrollo del detector de navegador automatizado (docs/Plan_Proteccion_Frente_a_Bots.md,
 * capa 2). Solo muestra los comandos para copiarlos: no consulta ni cambia nada. Abrir el pase exige
 * la llave SSH del servidor (scripts/pase-automatizacion.sh), asi que ver esta pagina no da acceso.
 */
const COMMANDS: readonly { id: string; labelKey: MessageKey; command: string }[] = [
  { id: 'on', labelKey: 'automationPass.on', command: 'bash scripts/pase-automatizacion.sh on' },
  {
    id: 'onHours',
    labelKey: 'automationPass.onHours',
    command: 'bash scripts/pase-automatizacion.sh on 8',
  },
  {
    id: 'state',
    labelKey: 'automationPass.state',
    command: 'bash scripts/pase-automatizacion.sh estado',
  },
  { id: 'off', labelKey: 'automationPass.off', command: 'bash scripts/pase-automatizacion.sh off' },
];

export default function AutomationPassPage() {
  return (
    <>
      <PageHeader title={t('automationPass.title')} description={t('automationPass.subtitle')} />
      <Card title={t('automationPass.commands')} description={t('automationPass.commandsHint')}>
        <div className="cf-stack">
          {COMMANDS.map((c) => (
            <div key={c.id}>
              <p>{t(c.labelKey)}</p>
              <div className="cf-inline">
                <code className="cf-mono">{c.command}</code>
                <CopyButton value={c.command} />
              </div>
            </div>
          ))}
        </div>
      </Card>
      <Alert tone="info" title={t('automationPass.safetyTitle')}>
        {t('automationPass.safety')}
      </Alert>
    </>
  );
}
