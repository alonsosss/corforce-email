import { describe, expect, it } from 'vitest';
import { FLAGS, type WebmailFolder } from '@/api/webmail';
import { t } from '@/i18n';
import { applyFlagChange } from './flags';
import { defaultFolder, folderLabel, orderFolders, showsRecipients } from './folders';
import { displayFilename, formatMailDate, parsePositiveInt } from './format';

function folder(name: string, role = '', selectable = true): WebmailFolder {
  return { name, delimiter: '/', role, selectable, total: 0, unread: 0 };
}

describe('carpetas del buzon', () => {
  it('bandeja primero, despues las especiales y el resto por nombre con sus subcarpetas', () => {
    const items = orderFolders([
      folder('Proyectos'),
      folder('Trash', 'trash'),
      folder('Proyectos/2026'),
      folder('INBOX', 'inbox'),
      folder('Sent', 'sent'),
      folder('Archivo viejo'),
    ]);
    expect(items.map((i) => i.folder.name)).toEqual([
      'INBOX',
      'Trash',
      'Sent',
      'Archivo viejo',
      'Proyectos',
      'Proyectos/2026',
    ]);
    expect(items.map((i) => i.depth)).toEqual([0, 0, 0, 0, 0, 1]);
    expect(items[0]?.label).toBe(t('webmail.role.inbox'));
    expect(items[5]?.label).toBe('2026');
  });

  it('una carpeta sin papel conocido se llama por su ultimo tramo', () => {
    expect(folderLabel(folder('Clientes/Norte'))).toBe('Norte');
    expect(folderLabel(folder('Otra', 'desconocido'))).toBe('Otra');
  });

  it('abre la bandeja de entrada, o la primera seleccionable si no la hay', () => {
    expect(defaultFolder([folder('Notas'), folder('INBOX', 'inbox')])).toBe('INBOX');
    expect(defaultFolder([folder('Grupo', '', false), folder('Notas')])).toBe('Notas');
    expect(defaultFolder([folder('Grupo', '', false)])).toBeNull();
  });

  it('en Enviados y Borradores importa el destinatario', () => {
    expect(showsRecipients('sent')).toBe(true);
    expect(showsRecipients('drafts')).toBe(true);
    expect(showsRecipients('inbox')).toBe(false);
  });
});

describe('formato del webmail', () => {
  it('quita del nombre de un adjunto los caracteres que disfrazan la extension', () => {
    const disguised = `factura${String.fromCharCode(0x202e)}fdp.exe`;
    expect(displayFilename(disguised, 'adjunto')).toBe('facturafdp.exe');
    expect(displayFilename(`a${String.fromCharCode(0)}b.txt`, 'adjunto')).toBe('ab.txt');
    expect(displayFilename('   ', 'adjunto')).toBe('adjunto');
  });

  it('solo acepta enteros positivos de la URL', () => {
    expect(parsePositiveInt('12')).toBe(12);
    expect(parsePositiveInt('0')).toBeNull();
    expect(parsePositiveInt('1e3')).toBeNull();
    expect(parsePositiveInt('-4')).toBeNull();
    expect(parsePositiveInt(null)).toBeNull();
  });

  it('la fecha del listado no inventa nada si el mensaje no la trae', () => {
    expect(formatMailDate(null)).toBe(t('common.dash'));
    expect(formatMailDate('no es fecha')).toBe(t('common.dash'));
  });

  it('aplica un cambio de flags sin repetir ninguno', () => {
    expect(applyFlagChange([FLAGS.seen], { add: [FLAGS.flagged, FLAGS.seen] })).toEqual([
      FLAGS.seen,
      FLAGS.flagged,
    ]);
    expect(applyFlagChange([FLAGS.seen, FLAGS.flagged], { remove: [FLAGS.seen] })).toEqual([
      FLAGS.flagged,
    ]);
  });
});
