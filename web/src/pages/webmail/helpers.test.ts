import { describe, expect, it } from 'vitest';
import type { Contact, FilterLimits, MailRule } from '@/api/webmail';
import { t } from '@/i18n';
import { initialBody, EMPTY_DRAFT } from './compose';
import {
  contactFromSender,
  contactProblems,
  emptyContact,
  formatBirthday,
} from './contacts/contacts';
import { folderNameProblem, isEmptiable, isProtectedFolder, joinFolderPath } from './folders';
import { mergeSuggestions } from './recipients';
import { cleanHtml, htmlHasContent, textToHtml } from './richText';
import { fromLocalInput, scheduleProblem, schedulePresets } from './schedule';
import {
  criteriaProblems,
  criteriaToFilters,
  criteriaToView,
  EMPTY_CRITERIA,
  readCriteria,
} from './search';
import { apiFieldError, emptyRule, forwardingProblems, ruleProblems } from './settings/filters';
import { INBOX, CLIENTES } from './testing';
import { ApiError } from '@/api/errors';

describe('busqueda avanzada en la URL', () => {
  it('lee los filtros de la query y descarta fechas mal formadas', () => {
    const criteria = readCriteria(
      new URLSearchParams('q=hola&from=luis&since=2026-09-01&before=2026-13-40&unread=1'),
    );
    expect(criteria).toMatchObject({ q: 'hola', from: 'luis', since: '2026-09-01', before: '' });
    expect(criteria.unread).toBe(true);
    expect(criteria.flagged).toBe(false);
  });

  it('escribe solo lo que hay y traduce al contrato del API', () => {
    const criteria = { ...EMPTY_CRITERIA, subject: 'Pedido', attachments: true };
    expect(criteriaToView(criteria)).toMatchObject({ subject: 'Pedido', attachments: '1' });
    expect(criteriaToView(criteria).from).toBeUndefined();
    expect(criteriaToFilters(criteria)).toMatchObject({ subject: 'Pedido', hasAttachments: true });
  });

  it('rechaza un rango invertido y un texto por encima del tope del servicio', () => {
    const problems = criteriaProblems(
      { ...EMPTY_CRITERIA, from: 'x'.repeat(10), since: '2026-09-10', before: '2026-09-01' },
      8,
    );
    expect(problems.from).toBe(t('webmail.list.searchTooLong'));
    expect(problems.before).toBe(t('webmail.search.badRange'));
    expect(criteriaProblems({ ...EMPTY_CRITERIA, q: 'ok' }, 8)).toEqual({});
  });
});

describe('HTML del editor', () => {
  it('reduce lo pegado a formato basico: sin scripts, estilos, atributos ni enlaces peligrosos', () => {
    const html = cleanHtml(
      '<p style="color:red" onclick="x()">Hola <b>Luis</b><script>alert(1)</script></p>' +
        '<a href="javascript:alert(1)">malo</a><a href="https://ok.test" target="_blank">bueno</a>' +
        '<img src="https://x.test/p.png"><h1>Titulo</h1>',
    );
    expect(html).toBe(
      '<p>Hola <b>Luis</b></p>malo<a href="https://ok.test">bueno</a><div>Titulo</div>',
    );
  });

  it('solo la firma conserva imagenes, y solo https o incrustadas', () => {
    expect(
      cleanHtml('<img src="https://x.test/l.png" alt="logo"><img src="http://x.test/a.png">', {
        images: true,
      }),
    ).toBe('<img src="https://x.test/l.png" alt="logo">');
  });

  it('convierte la cita de una respuesta en blockquote escapando el texto', () => {
    expect(textToHtml('Hola <b>\n> cita 1\n> cita 2')).toBe(
      '<div>Hola &lt;b&gt;</div><blockquote><div>cita 1</div><div>cita 2</div></blockquote>',
    );
    expect(htmlHasContent('<div><br></div>')).toBe(false);
    expect(htmlHasContent('<div>x</div>')).toBe(true);
  });
});

describe('firma al redactar', () => {
  const signature = { enabled: true, on_replies: false, html: '<b>Ana</b>', text: 'Ana' };

  it('va en los mensajes nuevos, en texto y en HTML', () => {
    const body = initialBody(EMPTY_DRAFT, null, signature);
    expect(body.text).toBe('\n\n-- \nAna');
    expect(body.html).toContain('<b>Ana</b>');
  });

  it('en respuestas solo si on_replies, y encima de la cita', () => {
    const seed = { ...EMPTY_DRAFT, text: '\n\nEl lunes, Luis escribio:\n> hola' };
    expect(initialBody(seed, 'reply', signature).text).toBe(seed.text);
    const withSignature = initialBody(seed, 'reply', { ...signature, on_replies: true });
    expect(withSignature.text.indexOf('Ana')).toBeLessThan(withSignature.text.indexOf('> hola'));
  });

  it('en una respuesta con formato va encima de la cita en HTML', () => {
    const seed = {
      ...EMPTY_DRAFT,
      text: '\n\nLuis escribio:\n> hola',
      html: '<div><br></div><blockquote><p>hola</p></blockquote>',
    };
    const body = initialBody(seed, 'reply', { ...signature, on_replies: true });
    expect(body.html.indexOf('<b>Ana</b>')).toBeLessThan(body.html.indexOf('<blockquote>'));
    expect(body.html).toContain('<blockquote><p>hola</p></blockquote>');
    expect(initialBody(seed, 'reply', signature).html).toBe(seed.html);
  });

  it('desactivada no se anade', () => {
    expect(initialBody(EMPTY_DRAFT, null, { ...signature, enabled: false }).text).toBe('');
  });
});

describe('envio programado', () => {
  const now = new Date(2026, 8, 24, 10, 0);

  it('ofrece atajos futuros y el lunes siguiente', () => {
    const presets = schedulePresets(now);
    expect(presets.every((p) => p.at > now)).toBe(true);
    expect(presets[2]?.at.getDay()).toBe(1);
  });

  it('valida contra el minuto minimo y el tope de dias de la meta', () => {
    expect(scheduleProblem(new Date(now.getTime() + 30_000), now, 30)).toBe(
      t('webmail.schedule.tooSoon'),
    );
    expect(scheduleProblem(new Date(2026, 10, 30), now, 30)).toBe(
      t('webmail.schedule.tooFar', { days: 30 }),
    );
    expect(scheduleProblem(new Date(2026, 8, 25, 8, 0), now, 30)).toBeNull();
    expect(scheduleProblem(fromLocalInput('no-es-fecha'), now, 30)).toBe(
      t('webmail.schedule.required'),
    );
  });
});

describe('carpetas propias', () => {
  it('protege INBOX y las carpetas con papel; solo Papelera y Spam se vacian', () => {
    expect(isProtectedFolder(INBOX)).toBe(true);
    expect(isProtectedFolder(CLIENTES)).toBe(false);
    expect(isEmptiable('trash')).toBe(true);
    expect(isEmptiable('inbox')).toBe(false);
  });

  it('valida el nombre como el servicio', () => {
    expect(joinFolderPath('Clientes', '2026', '/')).toBe('Clientes/2026');
    expect(folderNameProblem(' ', 'x', '/', 30)).toBe(t('webmail.folderAdmin.nameRequired'));
    expect(folderNameProblem('a/b', 'a/b', '/', 30)).toBe(
      t('webmail.folderAdmin.nameDelimiter', { delimiter: '/' }),
    );
    expect(folderNameProblem('a*', 'a*', '/', 30)).toBe(t('webmail.folderAdmin.nameInvalid'));
    expect(folderNameProblem('largo', 'x'.repeat(40), '/', 30)).toBe(
      t('webmail.folderAdmin.nameTooLong'),
    );
    expect(folderNameProblem('Clientes', 'Clientes', '/', 30)).toBeNull();
  });
});

const LIMITS: FilterLimits = {
  max_rules: 3,
  max_conditions: 2,
  max_actions: 2,
  max_forward_addresses: 2,
  max_value_length: 5,
  max_name_length: 8,
  max_folder_bytes: 10,
};

describe('reglas con los topes servidos', () => {
  const valid: MailRule = {
    ...emptyRule('Clientes'),
    name: 'Clientes',
    conditions: [{ field: 'from', op: 'contains', value: 'acme' }],
  };

  it('una regla dentro de los topes no tiene problemas', () => {
    expect(ruleProblems(valid, LIMITS, 'ana@empresa.com')).toEqual({});
  });

  it('senala cada campo con la ruta de details.field', () => {
    const problems = ruleProblems(
      {
        ...valid,
        name: 'Nombre demasiado largo',
        conditions: [
          { field: 'from', op: 'contains', value: 'muy largo' },
          { field: 'subject', op: 'is', value: '' },
          { field: 'to', op: 'is', value: 'x' },
        ],
        actions: [
          { type: 'discard' },
          { type: 'flag' },
          { type: 'forward', address: 'ana@empresa.com', keep_copy: true },
        ],
      },
      LIMITS,
      'ana@empresa.com',
    );
    expect(problems.name).toBe(t('webmail.rules.tooLong', { max: 8 }));
    expect(problems.conditions).toBe(t('webmail.rules.tooManyConditions', { max: 2 }));
    expect(problems['conditions[0].value']).toBe(t('webmail.rules.tooLong', { max: 5 }));
    expect(problems['conditions[1].value']).toBe(t('webmail.rules.valueRequired'));
    expect(problems.actions).toBe(t('webmail.rules.tooManyActions', { max: 2 }));
    expect(problems['actions[1].type']).toBe(t('webmail.rules.discardConflict'));
    expect(problems['actions[2].address']).toBe(t('webmail.rules.forwardToSelf'));
  });

  it('una accion repetida se rechaza salvo reenvios a direcciones distintas', () => {
    const repeated = ruleProblems(
      { ...valid, actions: [{ type: 'flag' }, { type: 'flag' }] },
      LIMITS,
    );
    expect(repeated['actions[1].type']).toBe(t('webmail.rules.actionRepeated'));
    const forwards = ruleProblems(
      {
        ...valid,
        actions: [
          { type: 'forward', address: 'a@x.com', keep_copy: true },
          { type: 'forward', address: 'b@x.com', keep_copy: true },
        ],
      },
      LIMITS,
    );
    expect(forwards).toEqual({});
  });

  it('el reenvio respeta su tope y exige direcciones si esta activo', () => {
    expect(forwardingProblems({ enabled: true, addresses: [], keep_copy: true }, LIMITS)).toEqual({
      addresses: t('webmail.forwarding.addressesRequired'),
    });
    expect(
      forwardingProblems(
        { enabled: true, addresses: ['a@x.co', 'b@x.co', 'c@x.co'], keep_copy: true },
        LIMITS,
      ).addresses,
    ).toBe(t('webmail.forwarding.tooMany', { max: 2 }));
  });

  it('un 422 con details.field se lleva a su campo; otro error no', () => {
    const invalid = new ApiError(
      422,
      { code: 'VALIDATION_ERROR', message: 'valor demasiado largo' },
      {
        error: {
          code: 'VALIDATION_ERROR',
          message: 'valor demasiado largo',
          details: { field: 'rules[2].conditions[0].value' },
        },
      },
    );
    expect(apiFieldError(invalid, 'rules[2].')).toEqual({
      'conditions[0].value': 'valor demasiado largo',
    });
    expect(apiFieldError(invalid, 'rules[1].')).toBeNull();
    expect(apiFieldError(new ApiError(503, null), 'rules[2].')).toBeNull();
  });
});

describe('contactos', () => {
  it('pide un nombre o un correo y valida correos y cumpleanos', () => {
    expect(contactProblems(emptyContact()).name).toBe(t('webmail.contacts.nameOrEmail'));
    const problems = contactProblems({
      ...emptyContact(),
      name: 'Luis',
      emails: [{ value: 'no-es-correo', type: 'work' }],
      birthday: '31/12',
    });
    expect(problems['emails[0].value']).toBe(t('webmail.contacts.emailInvalid'));
    expect(problems.birthday).toBe(t('webmail.contacts.birthdayInvalid'));
    expect(contactProblems({ ...emptyContact(), name: 'Luis', birthday: '--05-12' })).toEqual({});
  });

  it('anade el remitente con su nombre y su direccion', () => {
    expect(contactFromSender({ name: 'Luis', email: 'luis@cliente.com' })).toMatchObject({
      name: 'Luis',
      emails: [{ value: 'luis@cliente.com', type: 'other' }],
    });
    expect(formatBirthday('--05-12')).not.toMatch(/\d{4}/);
  });

  it('propone destinatarios de la agenda y de la empresa sin repetir direcciones', () => {
    const contact = {
      ...emptyContact(),
      id: 'c1',
      etag: 'e',
      updated_at: '',
      name: 'Luis',
      emails: [
        { value: 'luis@cliente.com', type: 'work' },
        { value: 'no valida', type: 'home' },
      ],
    } as Contact;
    const merged = mergeSuggestions(
      [contact],
      [
        { address: 'luis@cliente.com', display_name: 'Luis (empresa)' },
        { address: 'eva@empresa.com', display_name: '' },
      ],
    );
    expect(merged.map((s) => s.value)).toEqual(['luis@cliente.com', 'eva@empresa.com']);
    expect(merged[0]?.detail).toContain(t('webmail.suggest.personal'));
    expect(merged[1]?.label).toBe('eva@empresa.com');
  });
});
