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
  graphErrors,
  withFreshId,
  type StepDraft,
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
        { id: 's1', next: 's2', type: 'wait', duration: '3d' },
        {
          id: 's2',
          next: 's3',
          type: 'send_email',
          template_id: TEMPLATE,
          from_email: 'news@shop.example.com',
          from_name: 'Tienda',
        },
        { id: 's3', type: 'add_to_list', list_id: LIST },
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
    expect(empty.errors.steps).toBe(t('automations.steps.count', { min: 1, max: 40 }));

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

  it('arma una rama por apertura con sus dos salidas y la condicion', () => {
    const send: StepDraft = {
      ...emptyStep('send_email', meta),
      id: 'e',
      next: 'r',
      templateId: TEMPLATE,
      fromEmail: 'news@shop.example.com',
    };
    const branch: StepDraft = {
      ...emptyStep('branch', meta),
      id: 'r',
      then: 'si',
      else: '',
      conditionKind: 'email_opened',
      conditionStep: 'e',
    };
    const add: StepDraft = { ...emptyStep('add_to_list', meta), id: 'si', listId: LIST };
    const { request, errors } = buildRequest({ ...validDraft(), steps: [send, branch, add] }, meta);
    expect(errors).toEqual({});
    expect(request?.steps[1]).toEqual({
      id: 'r',
      type: 'branch',
      then: 'si',
      condition: { kind: 'email_opened', step: 'e' },
    });

    const noStep = { ...branch, conditionStep: '' };
    const bad = buildRequest({ ...validDraft(), steps: [send, noStep, add] }, meta);
    expect(bad.errors[`${noStep.key}.conditionStep`]).toBe(t('validation.required'));
  });

  it('arma las condiciones de segmento y de atributo', () => {
    const seg: StepDraft = {
      ...emptyStep('branch', meta),
      id: 'r',
      conditionKind: 'segment',
      conditionStep: undefined,
      segmentId: LIST,
    };
    expect(
      buildRequest({ ...validDraft(), steps: [seg] }, meta).request?.steps[0]?.condition,
    ).toEqual({
      kind: 'segment',
      segment_id: LIST,
    });
    const attr: StepDraft = {
      ...seg,
      conditionKind: 'attribute',
      segmentId: '',
      attribute: 'plan',
      op: 'eq',
      value: '"oro"',
    };
    expect(
      buildRequest({ ...validDraft(), steps: [attr] }, meta).request?.steps[0]?.condition,
    ).toEqual({
      kind: 'attribute',
      attribute: 'plan',
      op: 'eq',
      value: 'oro',
    });
    const broken = { ...attr, value: '{' };
    expect(
      buildRequest({ ...validDraft(), steps: [broken] }, meta).errors[`${broken.key}.value`],
    ).toBe(t('automations.condition.valueInvalid'));
  });

  it('valida el grafo en vivo: ciclos, pasos sueltos y envios que no dominan la rama', () => {
    const a: StepDraft = { ...emptyStep('wait', meta), id: 'a', next: 'b', amount: '1', unit: 'd' };
    const b: StepDraft = { ...emptyStep('wait', meta), id: 'b', next: 'a', amount: '1', unit: 'd' };
    const loop = graphErrors({ ...validDraft(), steps: [a, b] }, meta);
    expect(loop[`${a.key}.graph`]).toBe(t('automations.graph.cycle'));
    const lone: StepDraft = { ...emptyStep('wait', meta), id: 'c', amount: '1', unit: 'd' };
    const tail = { ...b, next: '' };
    expect(
      graphErrors({ ...validDraft(), steps: [a, tail, lone] }, meta)[`${lone.key}.graph`],
    ).toBe(t('automations.graph.unreachable'));
    const request = buildRequest({ ...validDraft(), steps: [a, b] }, meta);
    expect(request.request).toBeNull();
  });

  it('un paso nuevo recibe un id libre', () => {
    const steps = validDraft().steps.map((s, i) => ({ ...s, id: `s${i + 1}` }));
    expect(withFreshId(emptyStep('wait', meta), steps).id).toBe('s4');
  });

  it('el disparador por fecha exige atributo, hora y zona', () => {
    const draft = { ...validDraft(), triggerType: 'contact.date' };
    const { request, errors } = buildRequest(draft, meta);
    expect(request).toBeNull();
    expect(errors.dateAttribute).toBe(t('validation.required'));
    expect(errors.timezone).toBe(t('validation.required'));
    const ok = buildRequest(
      { ...draft, dateAttribute: 'cumple', hour: '8', timezone: 'America/Lima' },
      meta,
    );
    expect(ok.request?.trigger).toEqual({
      type: 'contact.date',
      attribute: 'cumple',
      hour: 8,
      timezone: 'America/Lima',
    });
    const late = buildRequest(
      { ...draft, dateAttribute: 'cumple', hour: '24', timezone: 'UTC' },
      meta,
    );
    expect(late.errors.hour).toBe(t('automations.editor.hourInvalid', { max: 23 }));
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
