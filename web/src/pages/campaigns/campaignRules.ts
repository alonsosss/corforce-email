import type { CampaignStatus } from '@/api/campaigns';

// Espejo de las transiciones de services/campaigns/internal/domain/campaign.go (ApplyPatch,
// CanSchedule, CanStart, Pause, Resume, Cancel, Deletable). Solo decide que acciones se
// ofrecen: el servicio vuelve a comprobarlas y responde 409 si ya no proceden.
const whenIn =
  (...states: CampaignStatus[]) =>
  (status: CampaignStatus): boolean =>
    states.includes(status);

export const campaignRules = {
  editable: whenIn('draft', 'paused'),
  /** En pausa ya hay destinatarios que recibieron una plantilla y una audiencia concretas. */
  contentLocked: whenIn('paused'),
  schedulable: whenIn('draft', 'scheduled'),
  startable: whenIn('draft', 'scheduled'),
  pausable: whenIn('sending', 'scheduled'),
  resumable: whenIn('paused'),
  cancellable: whenIn('scheduled', 'sending', 'paused'),
  deletable: whenIn('draft', 'cancelled'),
};

/** Espejo de domain.MaxTestRecipients: un envio de prueba admite de 1 a este numero. */
export const MAX_TEST_RECIPIENTS = 5;
