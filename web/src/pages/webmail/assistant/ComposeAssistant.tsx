import { useEffect, useRef, useState } from 'react';
import {
  assistantApi,
  type AssistantMessageRef,
  type AssistantText,
  type AssistantTone,
} from '@/api/webmailAssistant';
import { errorMessage } from '@/api/messages';
import { useAction } from '@/hooks/useAction';
import { Button } from '@/design/components';
import { IconReply } from '@/design/icons';
import { t, tEnum } from '@/i18n';
import { AssistantResult } from './AssistantResult';
import { charCount } from './assistant';
import { useAssistantStatus } from './useAssistantStatus';

type Result =
  | { kind: 'reply'; data: AssistantText }
  | { kind: 'tone'; tone: AssistantTone; data: AssistantText };

export interface ComposeAssistantProps {
  /** Texto actual del cuerpo, en plano: es lo unico que sale al cambiar el tono. */
  bodyText: () => string;
  /** Mensaje al que se responde; sin el no se ofrece proponer respuesta. */
  replyTo?: AssistantMessageRef;
  /** Respuesta propuesta desde el lector: se inserta una sola vez, al abrir la redaccion. */
  initialText?: string | null;
  disabled?: boolean;
  /** Pone el texto delante del cuerpo (encima de la cita). */
  onInsert: (text: string) => void;
  /** Sustituye el cuerpo por el texto. */
  onReplace: (text: string) => void;
}

/**
 * Asistente en la redaccion: cambiar el tono del borrador y proponer una respuesta. El resultado se
 * revisa y se inserta o se descarta; nunca se envia solo. Si el lector abrio la redaccion con una
 * respuesta propuesta, se inserta al abrir.
 */
export function ComposeAssistant({
  bodyText,
  replyTo,
  initialText = null,
  disabled = false,
  onInsert,
  onReplace,
}: ComposeAssistantProps) {
  const { status, reload } = useAssistantStatus();
  const [result, setResult] = useState<Result | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const seeded = useRef(false);

  useEffect(() => {
    if (seeded.current) return;
    seeded.current = true;
    if (initialText) onInsert(initialText);
    // Solo al abrir la redaccion.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const run = useAction(async (next: { kind: 'reply' } | { kind: 'tone'; tone: AssistantTone }) => {
    setResult(null);
    try {
      if (next.kind === 'reply' && replyTo) {
        setResult({ kind: 'reply', data: await assistantApi.reply(replyTo, '') });
      } else if (next.kind === 'tone') {
        setResult({
          kind: 'tone',
          tone: next.tone,
          data: await assistantApi.tone(bodyText(), next.tone),
        });
      }
    } finally {
      reload();
    }
  });

  if (!status) return null;

  const changeTone = (tone: AssistantTone) => {
    const text = bodyText().trim();
    if (!text) {
      setProblem(t('webmail.assistant.toneEmpty'));
      return;
    }
    if (charCount(text) > status.limits.max_input_chars) {
      setProblem(t('webmail.assistant.toneTooLong', { max: status.limits.max_input_chars }));
      return;
    }
    setProblem(null);
    void run.run({ kind: 'tone', tone });
  };

  const busy = disabled || run.busy;
  return (
    <section className="cf-wm-assistant" aria-label={t('webmail.assistant.title')}>
      <div className="cf-wm-assistant__bar">
        <span className="cf-wm-assistant__label">{t('webmail.assistant.tone.label')}</span>
        {status.tones.map((tone) => (
          <Button
            key={tone}
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={() => changeTone(tone)}
          >
            {tEnum('webmail.assistant.tone', tone)}
          </Button>
        ))}
        {replyTo ? (
          <Button
            size="sm"
            variant="ghost"
            icon={<IconReply size={16} />}
            disabled={busy}
            onClick={() => {
              setProblem(null);
              void run.run({ kind: 'reply' });
            }}
          >
            {t('webmail.assistant.reply')}
          </Button>
        ) : null}
        <span className="cf-wm-assistant__usage cf-text-sm cf-text-muted">
          {t('webmail.assistant.usage', {
            used: status.usage.mailbox,
            limit: status.limits.mailbox_daily,
          })}
        </span>
      </div>
      {run.busy ? (
        <p className="cf-text-sm cf-text-muted" role="status">
          {t('webmail.assistant.working')}
        </p>
      ) : null}
      {(problem ?? run.error) ? (
        <div className="cf-form__error" role="alert">
          {problem ?? errorMessage(run.error)}
        </div>
      ) : null}
      {result ? (
        <AssistantResult
          title={
            result.kind === 'reply'
              ? t('webmail.assistant.result.reply')
              : t('webmail.assistant.result.tone', {
                  tone: tEnum('webmail.assistant.tone', result.tone).toLowerCase(),
                })
          }
          text={result.data.text}
          inputTruncated={result.data.input_truncated}
          outputTruncated={result.data.output_truncated}
          onDiscard={() => setResult(null)}
          actions={
            <Button
              size="sm"
              disabled={disabled}
              onClick={() => {
                if (result.kind === 'reply') onInsert(result.data.text);
                else onReplace(result.data.text);
                setResult(null);
              }}
            >
              {t(
                result.kind === 'reply' ? 'webmail.assistant.insert' : 'webmail.assistant.replace',
              )}
            </Button>
          }
        />
      ) : null}
    </section>
  );
}
