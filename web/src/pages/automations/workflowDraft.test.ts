import { describe, expect, it } from 'vitest';
import type { Workflow } from '@/api/automations';
import { t } from '@/i18n';
import { automationsMetaFixture as meta } from './metaFixture';
import {
  buildRequest,
  draftFromWorkflow,
  emptyDraft,
  emptyStep,
  formatWait,
  moveStep,
  type WorkflowDraft,
} from './workflowDraft';

const TEMPLATE = '3f1c2b9e-7a64-4d2e-9b1f-2c3d4e5f6a7b';
const LIST = '8a7b6c5d-4e3f-4a2b-9c1d-0e9f8a7b6c5d';
const CAMPAIGN = '1b2c3d4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e';

function validDraft(): WorkflowDraft {
  const wait = { ...emptyStep('wait', meta), amount: '3', unit: 'd' };
  const send = {
    ...emptyStep('send_email', meta),
    templateId: TEMPLATE,
    fromEmail: 'news@shop.example.com',
    fromName: 'Tienda',
  };
  const add = { ...emptyStep('add_to_list', meta), listId: LIST };
  return {
    ...emptyDraft(meta),
    name: ' Bienvenida ',
    triggerType: 'contact.created',
    steps: [wait, send, add],
  };
}

describe('borrador de un flujo', () => {
  it('arma la peticion con los pasos, las unidades y los campos que admite cada tipo', () => {
    const { request, errors } = buildRequest(validDraft(), meta);
    expect(errors).toEqual({});
    expect(request).toEqual({
      name: 'Bienvenida',
      description: '',
      trigger: { type: 'contact.created' },
      list_id: null,
      re_entry: false,
      steps: [
        { type: 'wait', duration: '3d' },
        {
          type: 'send_email',
          template_id: TEMPLATE,
          from_email: 'news@shop.example.com',
          from_name: 'Tienda',
        },
        { type: 'add_to_list', list_id: LIST },
      ],
    });
  });

  it('solo manda la campana con un disparador que la admite', () => {
    const draft = { ...validDraft(), campaignId: CAMPAIGN };
    expect(buildRequest(draft, meta).request?.trigger).toEqual({ type: 'contact.created' });
    const clicked = { ...draft, triggerType: 'email.clicked' };
    expect(buildRequest(clicked, meta).request?.trigger).toEqual({
      type: 'email.clicked',
      campaign_id: CAMPAIGN,
    });
  });

  it('aplica los topes del catalogo: pasos y espera', () => {
    const empty = buildRequest({ ...validDraft(), steps: [] }, meta);
    expect(empty.request).toBeNull();
    expect(empty.errors.steps).toBe(t('automations.steps.count', { min: 1, max: 20 }));

    const draft = validDraft();
    const [wait] = draft.steps;
    if (!wait) throw new Error('falta el paso');
    wait.amount = '91';
    const { request, errors } = buildRequest(draft, meta);
    expect(request).toBeNull();
    expect(errors[`${wait.key}.amount`]).toBe(
      t('automations.wait.range', { min: formatWait(60, meta), max: formatWait(7776000, meta) }),
    );
  });

  it('rechaza lo que el servicio no aceptaria sin mandarlo', () => {
    const draft = validDraft();
    const [, send, add] = draft.steps;
    if (!send || !add) throw new Error('faltan pasos');
    send.templateId = '';
    send.templateVersion = '1.5';
    send.fromEmail = 'no-es-correo';
    add.listId = 'lista';
    const { request, errors } = buildRequest({ ...draft, name: '', listId: 'x' }, meta);
    expect(request).toBeNull();
    expect(errors.name).toBe(t('validation.required'));
    expect(errors.list).toBe(t('validation.uuid'));
    expect(errors[`${send.key}.template`]).toBe(t('validation.required'));
    expect(errors[`${send.key}.version`]).toBe(t('automations.step.versionInvalid'));
    expect(errors[`${send.key}.fromEmail`]).toBe(t('validation.email'));
    expect(errors[`${add.key}.list`]).toBe(t('validation.uuid'));
  });

  it('un flujo leido del API vuelve a producir los mismos pasos', () => {
    const { request } = buildRequest(validDraft(), meta);
    if (!request) throw new Error('peticion no valida');
    const workflow: Workflow = {
      ...request,
      steps: request.steps.map((s) =>
        s.type === 'send_email' ? { ...s, template_version: 4 } : s,
      ),
      id: 'w',
      tenant_id: 't',
      status: 'paused',
      pause_reason: 'manual',
      created_by: 'u',
      activated_at: null,
      created_at: '',
      updated_at: '',
    };
    const again = buildRequest(draftFromWorkflow(workflow, meta), meta);
    expect(again.errors).toEqual({});
    expect(again.request?.steps).toEqual(workflow.steps);
  });

  it('reordena los pasos sin salirse de la lista', () => {
    const steps = validDraft().steps;
    expect(moveStep(steps, 0, 1).map((s) => s.type)).toEqual(['send_email', 'wait', 'add_to_list']);
    expect(moveStep(steps, 0, -1)).toEqual(steps);
  });

  it('muestra una espera en la mayor unidad que la divide', () => {
    expect(formatWait(7776000, meta)).toBe(
      t('automations.wait.amount', { n: 90, unit: t('automations.waitUnitShort.d') }),
    );
    expect(formatWait(5400, meta)).toBe(
      t('automations.wait.amount', { n: 90, unit: t('automations.waitUnitShort.m') }),
    );
  });
});
