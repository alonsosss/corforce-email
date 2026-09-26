import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { User } from '@/api/identity';
import { t } from '@/i18n';
import { SYSTEM_USER_ID, UserLabel } from './useUserDirectory';

const ana: User = {
  id: '6f1c2d3e-0000-4000-8000-000000000001',
  email: 'ana@empresa.com',
  first_name: 'Ana',
  last_name: 'Ruiz',
  avatar_url: null,
  status: 'active',
  mfa_enabled: false,
  last_login_at: null,
  created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z',
};
const byId = new Map([[ana.id, ana]]);

describe('UserLabel', () => {
  it('nombra a la persona en vez de mostrar su id', () => {
    render(<UserLabel id={ana.id} byId={byId} />);
    expect(screen.getByText('Ana Ruiz')).toBeInTheDocument();
    expect(screen.getByText('ana@empresa.com')).toBeInTheDocument();
  });

  it('las acciones de la plataforma salen como Sistema', () => {
    render(<UserLabel id={SYSTEM_USER_ID} byId={byId} />);
    expect(screen.getByText(t('user.system'))).toBeInTheDocument();
  });

  it('sin la persona en la lista muestra el id', () => {
    render(<UserLabel id="otro-id" byId={byId} />);
    expect(screen.getByText('otro-id')).toBeInTheDocument();
  });
});
