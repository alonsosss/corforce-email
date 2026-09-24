import { useState, type FormEvent } from 'react';
import { webmailApi } from '@/api/webmail';
import {
  assistantApi,
  type AssistantExtraction,
  type AssistantMessageRef,
  type AssistantText,
} from '@/api/webmailAssistant';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { Button, ConfirmDialog, FormField, Input, useToast } from '@/design/components';
import { IconCalendar, IconFileText, IconReply } from '@/design/icons';
import { t } from '@/i18n';
import { eventProblems, formToInput, type EventForm } from '../calendar/calendar';
import { AssistantResult } from './AssistantResult';
import {
  charCount,
  describeWhen,
  eventFormFromExtraction,
  eventFormFromTask,
  todayKey,
} from './assistant';
import { useAssistantStatus } from './useAssistantStatus';

type Result =
  | { kind: 'summarize' | 'reply'; data: AssistantText }
  | { kind: 'extract'; data: AssistantExtraction };

export interface MessageAssistantProps {
  folder: string;
  uid: number;
  /** Mensajes del hilo, del mas antiguo al mas reciente; sin ellos se resume solo este mensaje. */
  thread?: AssistantMessageRef[];
  /** Abre la respuesta con el texto propuesto; el usuario lo revisa antes de enviar. */
  onReplyWith: (text: string) => void;
}

/**
 * Asistente en el lector: resumir, proponer respuesta y extraer tareas y citas. No se muestra si la
 * plataforma no lo ofrece o la empresa no lo activo. Nada de lo que propone se aplica solo: copiar,
 * responder o crear un evento es una accion explicita del usuario.
 */
export function MessageAssistant({ folder, uid, thread, onReplyWith }: MessageAssistantProps) {
  const toast = useToast();
  const { status, reload } = useAssistantStatus();
  const [askingReply, setAskingReply] = useState(false);
  const [instructions, setInstructions] = useState('');
  const [result, setResult] = useState<Result | null>(null);
  const [confirm, setConfirm] = useState<EventForm | null>(null);

  const ref: AssistantMessageRef = { folder, uid };
  const run = useAction(async (kind: Result['kind']) => {
    setResult(null);
    try {
      if (kind === 'summarize') {
        const messages = thread?.length ? thread : [ref];
        setResult({ kind, data: await assistantApi.summarize(messages) });
      } else if (kind === 'reply') {
        setResult({ kind, data: await assistantApi.reply(ref, instructions.trim()) });
        setAskingReply(false);
      } else {
        setResult({ kind, data: await assistantApi.extract(ref, todayKey()) });
      }
    } finally {
      reload();
    }
  });

  if (!status) return null;

  const maxInstructions = status.limits.max_instruction_chars;
  const instructionsTooLong = charCount(instructions.trim()) > maxInstructions;

  const submitReply = (e: FormEvent) => {
    e.preventDefault();
    if (!instructionsTooLong) void run.run('reply');
  };

  const propose = (form: EventForm | null) => {
    if (!form || Object.keys(eventProblems(form)).length > 0) {
      toast.error(t('webmail.assistant.extract.invalid'));
      return;
    }
    setConfirm(form);
  };

  return (
    <section className="cf-wm-assistant" aria-label={t('webmail.assistant.title')}>
      <div className="cf-wm-assistant__bar">
        <span className="cf-wm-assistant__label">{t('webmail.assistant.title')}</span>
        <Button
          size="sm"
          variant="ghost"
          icon={<IconFileText size={16} />}
          disabled={run.busy}
          onClick={() => void run.run('summarize')}
        >
          {t('webmail.assistant.summarize')}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          icon={<IconReply size={16} />}
          disabled={run.busy}
          aria-expanded={askingReply}
          onClick={() => setAskingReply((open) => !open)}
        >
          {t('webmail.assistant.reply')}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          icon={<IconCalendar size={16} />}
          disabled={run.busy}
          onClick={() => void run.run('extract')}
        >
          {t('webmail.assistant.extract')}
        </Button>
        <span className="cf-wm-assistant__usage cf-text-sm cf-text-muted">
          {t('webmail.assistant.usage', {
            used: status.usage.mailbox,
            limit: status.limits.mailbox_daily,
          })}
        </span>
      </div>
      {askingReply ? (
        <form className="cf-wm-assistant__ask" onSubmit={submitReply}>
          <FormField
            label={t('webmail.assistant.instructions')}
            htmlFor="wm-assistant-instructions"
            error={
              instructionsTooLong
                ? t('webmail.assistant.instructionsTooLong', { max: maxInstructions })
                : null
            }
          >
            <Input
              id="wm-assistant-instructions"
              value={instructions}
              placeholder={t('webmail.assistant.instructionsPlaceholder')}
              onChange={(e) => setInstructions(e.target.value)}
              disabled={run.busy}
            />
          </FormField>
          <Button type="submit" size="sm" loading={run.busy} disabled={instructionsTooLong}>
            {t('webmail.assistant.generate')}
          </Button>
        </form>
      ) : null}
      {run.busy ? (
        <p className="cf-text-sm cf-text-muted" role="status">
          {t('webmail.assistant.working')}
        </p>
      ) : null}
      {run.error ? (
        <div className="cf-form__error" role="alert">
          {errorMessage(run.error)}
        </div>
      ) : null}
      {result && result.kind !== 'extract' ? (
        <AssistantResult
          title={t(
            result.kind === 'reply'
              ? 'webmail.assistant.result.reply'
              : 'webmail.assistant.result.summarize',
          )}
          text={result.data.text}
          inputTruncated={result.data.input_truncated}
          outputTruncated={result.data.output_truncated}
          onDiscard={() => setResult(null)}
          actions={
            result.kind === 'reply' ? (
              <Button
                size="sm"
                icon={<IconReply size={14} />}
                onClick={() => onReplyWith(result.data.text)}
              >
                {t('webmail.assistant.replyWith')}
              </Button>
            ) : null
          }
        />
      ) : null}
      {result?.kind === 'extract' ? (
        <ExtractionResult data={result.data} onAdd={propose} onDiscard={() => setResult(null)} />
      ) : null}
      <ConfirmDialog
        open={confirm !== null}
        title={t('webmail.assistant.extract.confirmTitle')}
        message={
          confirm
            ? t('webmail.assistant.extract.confirmMessage', {
                title: confirm.title,
                when: describeWhen(confirm),
              })
            : ''
        }
        confirmLabel={t('webmail.assistant.extract.addToCalendar')}
        onCancel={() => setConfirm(null)}
        onConfirm={async () => {
          if (!confirm) return;
          await webmailApi.createCalendarEvent(formToInput(confirm));
          toast.success(t('webmail.assistant.extract.created'));
          setConfirm(null);
        }}
      />
    </section>
  );
}

function ExtractionResult({
  data,
  onAdd,
  onDiscard,
}: {
  data: AssistantExtraction;
  onAdd: (form: EventForm | null) => void;
  onDiscard: () => void;
}) {
  const empty = data.tasks.length === 0 && data.events.length === 0;
  return (
    <AssistantResult
      title={t('webmail.assistant.extract')}
      text={empty ? t('webmail.assistant.extract.none') : ''}
      inputTruncated={data.input_truncated}
      onDiscard={onDiscard}
    >
      {empty ? null : (
        <div className="cf-wm-assistant__extract">
          {data.events.length ? (
            <>
              <strong className="cf-text-sm">{t('webmail.assistant.extract.events')}</strong>
              <ul>
                {data.events.map((e, i) => (
                  <li key={`e${i}`}>
                    <span>
                      {e.title}
                      <span className="cf-text-muted">
                        {' '}
                        {e.date}
                        {e.start_time
                          ? ` ${e.start_time}${e.end_time ? `-${e.end_time}` : ''}`
                          : ` (${t('webmail.assistant.extract.allDay')})`}
                        {e.location ? `, ${e.location}` : ''}
                      </span>
                    </span>
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<IconCalendar size={14} />}
                      onClick={() => onAdd(eventFormFromExtraction(e))}
                    >
                      {t('webmail.assistant.extract.addToCalendar')}
                    </Button>
                  </li>
                ))}
              </ul>
            </>
          ) : null}
          {data.tasks.length ? (
            <>
              <strong className="cf-text-sm">{t('webmail.assistant.extract.tasks')}</strong>
              <ul>
                {data.tasks.map((task, i) => (
                  <li key={`t${i}`}>
                    <span>
                      {task.title}
                      <span className="cf-text-muted">
                        {' '}
                        {task.due_date || t('webmail.assistant.extract.noDate')}
                      </span>
                    </span>
                    {task.due_date ? (
                      <Button
                        size="sm"
                        variant="ghost"
                        icon={<IconCalendar size={14} />}
                        onClick={() => onAdd(eventFormFromTask(task))}
                      >
                        {t('webmail.assistant.extract.addToCalendar')}
                      </Button>
                    ) : null}
                  </li>
                ))}
              </ul>
            </>
          ) : null}
        </div>
      )}
    </AssistantResult>
  );
}
