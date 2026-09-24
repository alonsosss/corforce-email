import { describe, expect, it } from 'vitest';
import { t } from '@/i18n';
import { companyFromAddress, namesOf, quickReplyValues, resolveQuickReply } from './quickReplies';
import { canSnooze, followUpChoices, snoozePresets, snoozeProblem } from './snooze';

describe('posponer', () => {
  it('ofrece esta tarde solo si falta al menos una hora, y luego manana y el lunes', () => {
    // Miercoles 24 de septiembre de 2026 a las 10:00, hora local.
    const morning = new Date(2026, 8, 24, 10, 0);
    const ids = snoozePresets(morning).map((p) => p.id);
    expect(ids).toEqual(['this-afternoon', 'tomorrow', 'monday']);
    const [afternoon, tomorrow, monday] = snoozePresets(morning);
    expect(afternoon?.at.getHours()).toBe(17);
    expect(tomorrow?.at.getDate()).toBe(25);
    expect(tomorrow?.at.getHours()).toBe(8);
    expect(monday?.at.getDay()).toBe(1);
    expect(monday?.at.getDate()).toBe(28);

    const late = new Date(2026, 8, 24, 16, 30);
    expect(snoozePresets(late).map((p) => p.id)).toEqual(['tomorrow', 'monday']);
    // Un lunes, "el lunes" es el de la semana siguiente.
    const monday2 = new Date(2026, 8, 28, 9, 0);
    expect(
      snoozePresets(monday2)
        .find((p) => p.id === 'monday')
        ?.at.getDate(),
    ).toBe(5);
  });

  it('valida la hora de vuelta contra el tope del servicio', () => {
    const now = new Date(2026, 8, 24, 10, 0);
    expect(snoozeProblem(null, now, 30)).toBe(t('webmail.snooze.required'));
    expect(snoozeProblem(new Date(now.getTime() + 30_000), now, 30)).toBe(
      t('webmail.snooze.tooSoon'),
    );
    expect(snoozeProblem(new Date(now.getTime() + 31 * 86_400_000), now, 30)).toBe(
      t('webmail.snooze.tooFar', { days: 30 }),
    );
    expect(snoozeProblem(new Date(now.getTime() + 3_600_000), now, 30)).toBeNull();
    expect(snoozeProblem(new Date(now.getTime() + 400 * 86_400_000), now, null)).toBeNull();
  });

  it('no se pospone desde Pospuestos, Programados ni Borradores', () => {
    expect(canSnooze('inbox')).toBe(true);
    expect(canSnooze('')).toBe(true);
    expect(canSnooze('sent')).toBe(true);
    for (const role of ['snoozed', 'scheduled', 'drafts']) expect(canSnooze(role)).toBe(false);
  });

  it('los plazos de seguimiento caben en el tope servido', () => {
    expect(followUpChoices(null)).toEqual([1, 3, 7]);
    expect(followUpChoices(5)).toEqual([1, 3]);
  });
});

describe('respuestas rapidas', () => {
  const mailbox = { email: 'ana.perez@empresa.com', name: 'Ana Perez' };
  const now = new Date(2026, 8, 24, 10, 0);

  it('resuelve las variables con el destinatario y el buzon, y escapa en HTML', () => {
    const values = quickReplyValues({
      recipient: 'Luis@Cliente.com.pe',
      names: { 'luis@cliente.com.pe': 'Luis <Jefe>' },
      mailbox,
      now,
    });
    expect(values.nombre).toBe('Luis <Jefe>');
    expect(values.empresa).toBe('Cliente');
    expect(values.correo).toBe('luis@cliente.com.pe');
    expect(values.mi_nombre).toBe('Ana Perez');
    expect(values.mi_correo).toBe('ana.perez@empresa.com');
    expect(values.fecha).toContain('2026');

    const html = resolveQuickReply('<p>Hola {nombre} de {empresa}, {otra}</p>', values, true);
    expect(html).toBe('<p>Hola Luis &lt;Jefe&gt; de Cliente, {otra}</p>');
    expect(resolveQuickReply('Hola {nombre}', values, false)).toBe('Hola Luis <Jefe>');
  });

  it('sin nombre conocido lo saca de la direccion; sin destinatario deja la variable', () => {
    const values = quickReplyValues({ recipient: 'maria.lopez@acme.com', mailbox, now });
    expect(values.nombre).toBe('Maria Lopez');
    expect(values.empresa).toBe('Acme');
    const empty = quickReplyValues({ mailbox: { email: 'ana@empresa.com', name: ' ' }, now });
    expect(resolveQuickReply('Hola {nombre}, {mi_nombre}', empty, false)).toBe(
      'Hola {nombre}, Ana',
    );
  });

  it('deduce la empresa del dominio', () => {
    expect(companyFromAddress('a@mail.acme.com')).toBe('Acme');
    expect(companyFromAddress('a@cliente.pe')).toBe('Cliente');
    expect(companyFromAddress('a@acme.co.uk')).toBe('Acme');
    expect(companyFromAddress('a@localhost')).toBe('');
  });

  it('toma los nombres visibles del mensaje al que se responde', () => {
    const names = namesOf({
      from: [{ name: 'Luis', email: 'Luis@x.com' }],
      to: [{ name: '', email: 'ana@y.com' }],
      cc: [{ name: 'Eva', email: 'eva@x.com' }],
      reply_to: [{ name: 'Ventas', email: 'luis@x.com' }],
    });
    expect(names).toEqual({ 'luis@x.com': 'Ventas', 'eva@x.com': 'Eva' });
  });
});
