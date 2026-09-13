import { describe, expect, it } from 'vitest';
import type { SuppressionCause, SuppressionEntry, SuppressionReason } from '@/api/suppression';
import { activeCausesAfterRemoving, causeViews } from './causes';

const NOW = new Date('2026-09-13T12:00:00Z');

function cause(id: string, reason: SuppressionReason, expires?: string): SuppressionCause {
  return {
    id,
    tenant_id: 't',
    email: 'ana@cliente.com',
    reason,
    source: '',
    detail: '',
    expires_at: expires,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  };
}

describe('causas de una exclusion', () => {
  it('la principal primero, despues las vigentes y al final las caducadas', () => {
    const entry: SuppressionEntry = {
      ...cause('p', 'hard_bounce'),
      reasons: ['hard_bounce', 'unsubscribe'],
      causes: [
        cause('m', 'manual', '2026-01-01T00:00:00Z'),
        cause('p', 'hard_bounce'),
        cause('u', 'unsubscribe'),
      ],
    };
    const views = causeViews(entry, NOW);
    expect(views.map((v) => v.cause.id)).toEqual(['p', 'u', 'm']);
    expect(views.map((v) => v.primary)).toEqual([true, false, false]);
    expect(views.map((v) => v.active)).toEqual([true, true, false]);
  });

  it('una manual con caducidad futura sigue vigente', () => {
    const entry: SuppressionEntry = {
      ...cause('m', 'manual', '2027-01-01T00:00:00Z'),
      reasons: ['manual'],
      causes: [cause('m', 'manual', '2027-01-01T00:00:00Z')],
    };
    expect(causeViews(entry, NOW)[0]).toMatchObject({ primary: true, active: true });
  });

  it('sin la lista de causas, la fila es su unica causa', () => {
    const entry: SuppressionEntry = { ...cause('x', 'complaint'), reasons: null, causes: null };
    expect(causeViews(entry, NOW).map((v) => v.cause.id)).toEqual(['x']);
  });

  it('cuenta las causas vigentes que quedarian al retirar una', () => {
    const entry: SuppressionEntry = {
      ...cause('p', 'complaint'),
      reasons: ['complaint', 'manual'],
      causes: [cause('p', 'complaint'), cause('m', 'manual')],
    };
    expect(activeCausesAfterRemoving(entry, 'm')).toBe(1);
    const single: SuppressionEntry = {
      ...entry,
      reasons: ['manual'],
      causes: [cause('m', 'manual')],
    };
    expect(activeCausesAfterRemoving(single, 'm')).toBe(0);
  });
});
