import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import type { PermissionTriple } from '@/api/access';
import { ApiError, ERROR_CODES } from '@/api/errors';
import { mailSecurityApi, type QuarantineSettings } from '@/api/mailSecurity';
import { MODULES } from '@/access/modules';
import { PERMISSIONS } from '@/access/permissions';
import { useAccessStore } from '@/access/store';
import { ToastProvider } from '@/design/components';
import { t } from '@/i18n';
import { QuarantineSettingsTab } from './QuarantineSettingsTab';
import { noticeFieldError } from './quarantineNotice';

const SETTINGS: QuarantineSettings = {
  tenant_id: 't',
  max_size_bytes: 10 * 1024 * 1024,
  max_age_days: 30,
  retention_size: 100,
  exclude_domains: [],
  notify: {
    enabled: true,
    max_score: '9999',
    sender: 'avisos@empresa.com',
    subject: 'Correo retenido',
    html_template: '<p>{{.Count}} mensajes</p>',
  },
  updated_at: '2026-09-13T10:00:00Z',
};

const validation = (message: string) =>
  new ApiError(422, { code: ERROR_CODES.VALIDATION_ERROR, message });

describe('errores del aviso de cuarentena', () => {
  it('asocia el 422 al campo que nombra el servicio', () => {
    expect(
      noticeFieldError(validation('notify.sender debe ser una dirección de correo válida')),
    ).toEqual({
      field: 'sender',
      message: 'notify.sender debe ser una dirección de correo válida',
    });
    expect(noticeFieldError(validation('notify.subject es obligatorio'))?.field).toBe('subject');
    expect(
      noticeFieldError(validation('notify.html_template no se puede ejecutar: x'))?.field,
    ).toBe('html_template');
  });

  it('lo que no es un campo del aviso no se asocia a ninguno', () => {
    expect(noticeFieldError(validation('max_age_days debe ser positivo'))).toBeNull();
    expect(noticeFieldError(validation('notify.max_score invalido'))).toBeNull();
    expect(
      noticeFieldError(new ApiError(409, { code: ERROR_CODES.CONFLICT, message: 'notify.sender' })),
    ).toBeNull();
    expect(noticeFieldError(new Error('notify.sender'))).toBeNull();
  });
});

describe('ajustes de cuarentena con el aviso activo', () => {
  afterEach(() => {
    useAccessStore.getState().reset();
    vi.restoreAllMocks();
  });

  function grantEdit() {
    const [module, resource, action] = PERMISSIONS.quarantineSettings.update;
    const permissions: PermissionTriple[] = [{ module, resource, action }];
    useAccessStore.setState({
      loaded: true,
      isAdmin: false,
      policyLoaded: true,
      modules: [MODULES.mailSecurity],
      permissions,
    });
  }

  function renderTab() {
    render(
      <MemoryRouter>
        <ToastProvider>
          <QuarantineSettingsTab />
        </ToastProvider>
      </MemoryRouter>,
    );
  }

  it('pinta el 422 junto al campo que lo causa, no como error general', async () => {
    const user = userEvent.setup();
    grantEdit();
    vi.spyOn(mailSecurityApi, 'getQuarantineSettings').mockResolvedValue({ data: SETTINGS });
    const message = 'notify.html_template no es una plantilla valida: unexpected "}" in operand';
    vi.spyOn(mailSecurityApi, 'putQuarantineSettings').mockRejectedValue(validation(message));
    renderTab();

    await user.click(await screen.findByRole('button', { name: t('common.save') }));

    const template = screen.getByLabelText(
      new RegExp(`^${t('security.quarantineSettings.notifyTemplate')}`),
    );
    expect(await screen.findByText(message)).toHaveAttribute('id', 'q-template-error');
    expect(template).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getAllByText(message)).toHaveLength(1);

    await user.type(template, ' ');
    expect(screen.queryByText(message)).toBeNull();
  });

  it('con el aviso activo exige remitente, asunto y plantilla antes de enviar', async () => {
    const user = userEvent.setup();
    grantEdit();
    vi.spyOn(mailSecurityApi, 'getQuarantineSettings').mockResolvedValue({
      data: { ...SETTINGS, notify: { ...SETTINGS.notify, subject: '' } },
    });
    const put = vi.spyOn(mailSecurityApi, 'putQuarantineSettings');
    renderTab();

    await user.click(await screen.findByRole('button', { name: t('common.save') }));

    expect(await screen.findByText(t('validation.required'))).toHaveAttribute(
      'id',
      'q-subject-error',
    );
    expect(put).not.toHaveBeenCalled();
  });

  it('lista las variables que el servicio entrega a la plantilla', async () => {
    grantEdit();
    vi.spyOn(mailSecurityApi, 'getQuarantineSettings').mockResolvedValue({ data: SETTINGS });
    renderTab();
    for (const name of [
      '.Mailbox',
      '.Count',
      '.LinksExpireAt',
      '.Subject',
      '.ReleaseURL',
      '.DiscardURL',
    ]) {
      expect(await screen.findByText(`{{${name}}}`)).toBeInTheDocument();
    }
  });
});
