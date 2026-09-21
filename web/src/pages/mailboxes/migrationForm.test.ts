import { describe, expect, it } from 'vitest';
import {
  changeMigrationPort,
  initialMigrationForm,
  takeMigrationSubmission,
  validateMigrationForm,
} from './migrationForm';
import { META } from './migrationFixtures';

const FILLED = {
  host: '  imap.origen.test ',
  port: '993',
  tls: 'ssl',
  username: ' ana@origen.test ',
  password: 'secreto de origen',
};

describe('estado inicial del formulario', () => {
  it('parte del puerto con TLS implicito que ofrece el servicio', () => {
    expect(initialMigrationForm(META)).toEqual({
      host: '',
      port: '993',
      tls: 'ssl',
      username: '',
      password: '',
    });
  });

  it('si ningun puerto es implicito toma el primero y su TLS habitual', () => {
    const meta = { ...META, default_tls_for_port: { '143': 'starttls' } };
    expect(initialMigrationForm(meta)).toMatchObject({ port: '143', tls: 'starttls' });
  });

  it('sin puertos no inventa ninguno', () => {
    expect(initialMigrationForm({ ...META, source_ports: [] })).toMatchObject({ port: '' });
  });
});

describe('cambio de puerto', () => {
  it('el TLS pasa al habitual del puerto elegido', () => {
    const form = { ...FILLED, tls: 'ssl', port: '993' };
    expect(changeMigrationPort(form, '143', META)).toMatchObject({ port: '143', tls: 'starttls' });
    expect(
      changeMigrationPort({ ...form, port: '143', tls: 'starttls' }, '993', META),
    ).toMatchObject({ port: '993', tls: 'ssl' });
  });

  it('no impone un modo que el servicio no permite y conserva el elegido', () => {
    const meta = { ...META, source_tls_modes: ['ssl'] };
    expect(changeMigrationPort({ ...FILLED, tls: 'ssl' }, '143', meta).tls).toBe('ssl');
  });

  it('conserva el resto de lo escrito, contrasena incluida', () => {
    expect(changeMigrationPort(FILLED, '143', META)).toMatchObject({
      host: FILLED.host,
      username: FILLED.username,
      password: FILLED.password,
    });
  });
});

describe('validacion', () => {
  it('todo relleno y dentro de las opciones del servicio no da errores', () => {
    const errors = validateMigrationForm(FILLED, META);
    expect(Object.values(errors).filter(Boolean)).toEqual([]);
  });

  it('exige servidor, usuario y contrasena', () => {
    const errors = validateMigrationForm(
      { ...FILLED, host: '  ', username: '', password: '' },
      META,
    );
    expect(errors.host).toBeTruthy();
    expect(errors.username).toBeTruthy();
    expect(errors.password).toBeTruthy();
    expect(errors.port).toBeUndefined();
  });

  it('rechaza un puerto o un TLS que el servicio no ofrece', () => {
    const errors = validateMigrationForm({ ...FILLED, port: '25', tls: 'none' }, META);
    expect(errors.port).toBeTruthy();
    expect(errors.tls).toBeTruthy();
  });
});

describe('envio', () => {
  it('la peticion lleva la contrasena tal cual y el resto sin espacios sobrantes', () => {
    const { request } = takeMigrationSubmission('mb-1', FILLED);
    expect(request).toEqual({
      mailbox_id: 'mb-1',
      source_host: 'imap.origen.test',
      source_port: 993,
      source_tls: 'ssl',
      source_username: 'ana@origen.test',
      source_password: 'secreto de origen',
    });
  });

  it('el formulario resultante conserva lo escrito pero sin la contrasena', () => {
    const { next } = takeMigrationSubmission('mb-1', FILLED);
    expect(next).toEqual({ ...FILLED, password: '' });
  });

  it('no modifica el estado que recibe', () => {
    const before = { ...FILLED };
    takeMigrationSubmission('mb-1', FILLED);
    expect(FILLED).toEqual(before);
  });
});
