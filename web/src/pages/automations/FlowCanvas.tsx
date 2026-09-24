import { useId, useState, type DragEvent, type ReactNode } from 'react';
import type { StepType } from '@/api/automations';
import {
  IconMail,
  IconMinus,
  IconPause,
  IconPlay,
  IconPlus,
  IconRoute,
  IconUsers,
} from '@/design/icons';
import { t, tEnum } from '@/i18n';
import { layoutGraph, type Anchor, type Edge, type Port } from './workflowGraph';
import type { StepDraft } from './workflowDraft';

// Lienzo del flujo sin dependencias: los pasos se colocan por capas (workflowGraph) en
// posiciones absolutas y las conexiones se dibujan en un SVG por encima. Un paso se anade
// arrastrandolo desde la paleta hasta un hueco (+) de una conexion o eligiendolo en la paleta
// y pulsando el hueco, que tambien funciona con teclado. Nada usa HTML ni estilos en linea
// interpretados: las posiciones van por la API de estilos del DOM, que la CSP permite.

const NODE_W = 208;
const NODE_H = 68;
const GAP_X = 56;
const GAP_Y = 64;
const PAD = 24;
const DRAG_TYPE = 'application/x-cf-step-type';

const STEP_ICONS: Record<StepType, ReactNode> = {
  wait: <IconPause size={16} />,
  send_email: <IconMail size={16} />,
  add_to_list: <IconUsers size={16} />,
  remove_from_list: <IconMinus size={16} />,
  branch: <IconRoute size={16} />,
};

export interface FlowCanvasProps {
  steps: StepDraft[];
  stepTypes: StepType[];
  triggerLabel: string;
  selected: string | null;
  onSelect: (key: string | null) => void;
  /** Inserta un paso del tipo en el hueco; null en solo lectura. */
  onInsert: ((anchor: Anchor, type: StepType) => void) | null;
  /** Resumen de una linea del paso (plantilla, espera, lista, condicion). */
  summary: (step: StepDraft) => string;
  /** Pasos con algun error (de campos o de grafo). */
  invalid: ReadonlySet<string>;
  maxSteps: number;
}

interface Point {
  x: number;
  y: number;
}

export function FlowCanvas({
  steps,
  stepTypes,
  triggerLabel,
  selected,
  onSelect,
  onInsert,
  summary,
  invalid,
  maxSteps,
}: FlowCanvasProps) {
  const markerId = `${useId().replace(/:/g, '')}-arrow`;
  const [armed, setArmed] = useState<StepType | null>(null);
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  const layout = layoutGraph(steps);
  const full = steps.length >= maxSteps;
  const editable = onInsert !== null && !full;

  const layerCount = new Map<number, number>();
  for (const p of layout.placed.values())
    layerCount.set(p.layer, (layerCount.get(p.layer) ?? 0) + 1);
  const width = Math.max(1, layout.columns) * (NODE_W + GAP_X) - GAP_X + PAD * 2;
  const height = (layout.layers + 1) * (NODE_H + GAP_Y) + NODE_H / 2 + PAD * 2;
  const topLeft = (key: string | null): Point => {
    if (key === null) return { x: width / 2 - NODE_W / 2, y: PAD };
    const p = layout.placed.get(key);
    if (!p) return { x: PAD, y: PAD };
    const inLayer = layerCount.get(p.layer) ?? 1;
    const offset = ((layout.columns - inLayer) * (NODE_W + GAP_X)) / 2;
    return {
      x: PAD + offset + p.column * (NODE_W + GAP_X),
      y: PAD + (p.layer + 1) * (NODE_H + GAP_Y),
    };
  };
  const portPoint = (from: string | null, port: Port): Point => {
    const o = topLeft(from);
    const fraction = port === 'then' ? 0.28 : port === 'else' ? 0.72 : 0.5;
    return { x: o.x + NODE_W * fraction, y: o.y + NODE_H };
  };
  const endPoint = (edge: Edge): Point => {
    if (edge.to) {
      const o = topLeft(edge.to);
      return { x: o.x + NODE_W / 2, y: o.y };
    }
    const start = portPoint(edge.from, edge.port);
    return { x: start.x, y: start.y + GAP_Y * 0.6 };
  };
  const slotKey = (edge: Edge) => `${edge.from ?? 'trigger'}:${edge.port}`;
  const insert = (edge: Edge, type: StepType) => {
    onInsert?.({ from: edge.from, port: edge.port }, type);
    setArmed(null);
    setDropTarget(null);
  };
  const onDrop = (edge: Edge) => (e: DragEvent) => {
    e.preventDefault();
    const type = e.dataTransfer.getData(DRAG_TYPE) as StepType;
    if (stepTypes.includes(type)) insert(edge, type);
  };

  return (
    <div className="cf-flow">
      {onInsert ? (
        <div
          className="cf-flow__palette"
          role="toolbar"
          aria-label={t('automations.canvas.palette')}
        >
          <span className="cf-text-sm cf-text-secondary">
            {full
              ? t('automations.editor.maxSteps', { max: maxSteps })
              : armed
                ? t('automations.canvas.armed', { type: tEnum('automations.stepType', armed) })
                : t('automations.canvas.paletteHint')}
          </span>
          {stepTypes.map((type) => (
            <button
              key={type}
              type="button"
              className={`cf-flow__tool${armed === type ? ' cf-flow__tool--armed' : ''}`}
              draggable={!full}
              disabled={full}
              aria-pressed={armed === type}
              onClick={() => setArmed(armed === type ? null : type)}
              onDragStart={(e) => {
                e.dataTransfer.setData(DRAG_TYPE, type);
                e.dataTransfer.effectAllowed = 'copy';
              }}
            >
              {STEP_ICONS[type]}
              <span>{tEnum('automations.stepType', type)}</span>
            </button>
          ))}
        </div>
      ) : null}
      <div className="cf-flow__viewport">
        <div
          className="cf-flow__canvas"
          ref={(el) => {
            if (el) {
              el.style.width = `${width}px`;
              el.style.height = `${height}px`;
            }
          }}
        >
          <svg className="cf-flow__edges" width={width} height={height} aria-hidden="true">
            <defs>
              <marker
                id={markerId}
                viewBox="0 0 10 10"
                refX="9"
                refY="5"
                markerWidth="7"
                markerHeight="7"
                orient="auto-start-reverse"
              >
                <path d="M 0 0 L 10 5 L 0 10 z" className="cf-flow__arrow" />
              </marker>
            </defs>
            {layout.edges.map((edge) => {
              const a =
                edge.from === null
                  ? { x: width / 2, y: PAD + NODE_H }
                  : portPoint(edge.from, edge.port);
              const b = endPoint(edge);
              const mid = Math.max(24, (b.y - a.y) / 2);
              const d = `M ${a.x} ${a.y} C ${a.x} ${a.y + mid}, ${b.x} ${b.y - mid}, ${b.x} ${b.y}`;
              return (
                <g key={slotKey(edge)}>
                  <path
                    d={d}
                    className={`cf-flow__edge cf-flow__edge--${edge.port}`}
                    markerEnd={edge.to ? `url(#${markerId})` : undefined}
                  />
                  {edge.port !== 'next' ? (
                    <text
                      x={a.x + (edge.port === 'then' ? -6 : 6)}
                      y={a.y + 14}
                      className="cf-flow__label"
                      textAnchor={edge.port === 'then' ? 'end' : 'start'}
                    >
                      {t(
                        edge.port === 'then'
                          ? 'automations.canvas.then'
                          : 'automations.canvas.else',
                      )}
                    </text>
                  ) : null}
                  {edge.to === null ? (
                    <circle cx={b.x} cy={b.y} r={4} className="cf-flow__end" />
                  ) : null}
                </g>
              );
            })}
          </svg>
          <FlowBox point={topLeft(null)} className="cf-flow__node cf-flow__node--trigger">
            <span className="cf-flow__node-type">
              <IconPlay size={16} />
              {t('automations.editor.trigger')}
            </span>
            <span className="cf-flow__node-summary">{triggerLabel}</span>
          </FlowBox>
          {steps.map((step, index) => (
            <FlowBox
              key={step.key}
              point={topLeft(step.key)}
              className={[
                'cf-flow__node',
                step.type === 'branch' ? 'cf-flow__node--branch' : '',
                selected === step.key ? 'cf-flow__node--selected' : '',
                invalid.has(step.key) ? 'cf-flow__node--invalid' : '',
              ]
                .filter(Boolean)
                .join(' ')}
            >
              <button
                type="button"
                className="cf-flow__node-button"
                aria-pressed={selected === step.key}
                aria-label={t('automations.canvas.select', {
                  n: index + 1,
                  type: tEnum('automations.stepType', step.type),
                })}
                onClick={() => onSelect(selected === step.key ? null : step.key)}
              >
                <span className="cf-flow__node-type">
                  {STEP_ICONS[step.type]}
                  {tEnum('automations.stepType', step.type)}
                  <span className="cf-flow__node-id cf-mono">{step.id}</span>
                </span>
                <span className="cf-flow__node-summary">{summary(step)}</span>
              </button>
            </FlowBox>
          ))}
          {editable
            ? layout.edges.map((edge) => {
                const a =
                  edge.from === null
                    ? { x: width / 2, y: PAD + NODE_H }
                    : portPoint(edge.from, edge.port);
                const b = endPoint(edge);
                const key = slotKey(edge);
                return (
                  <SlotButton
                    key={key}
                    point={{ x: (a.x + b.x) / 2, y: edge.to ? (a.y + b.y) / 2 : b.y }}
                    active={dropTarget === key}
                    label={t('automations.canvas.insert', {
                      where:
                        edge.from === null
                          ? t('automations.canvas.afterTrigger')
                          : tEnum('automations.canvas.port', edge.port),
                    })}
                    disabled={!armed}
                    onClick={() => {
                      if (armed) insert(edge, armed);
                    }}
                    onDragOver={(e) => {
                      if (e.dataTransfer.types.includes(DRAG_TYPE)) {
                        e.preventDefault();
                        e.dataTransfer.dropEffect = 'copy';
                        setDropTarget(key);
                      }
                    }}
                    onDragLeave={() => setDropTarget((k) => (k === key ? null : k))}
                    onDrop={onDrop(edge)}
                  />
                );
              })
            : null}
        </div>
      </div>
    </div>
  );
}

function FlowBox({
  point,
  className,
  children,
}: {
  point: Point;
  className: string;
  children: ReactNode;
}) {
  return (
    <div
      className={className}
      ref={(el) => {
        if (el) {
          el.style.left = `${point.x}px`;
          el.style.top = `${point.y}px`;
          el.style.width = `${NODE_W}px`;
          el.style.height = `${NODE_H}px`;
        }
      }}
    >
      {children}
    </div>
  );
}

function SlotButton({
  point,
  active,
  label,
  disabled,
  onClick,
  onDragOver,
  onDragLeave,
  onDrop,
}: {
  point: Point;
  active: boolean;
  label: string;
  disabled: boolean;
  onClick: () => void;
  onDragOver: (e: DragEvent) => void;
  onDragLeave: () => void;
  onDrop: (e: DragEvent) => void;
}) {
  return (
    <button
      type="button"
      className={`cf-flow__slot${active ? ' cf-flow__slot--active' : ''}${disabled ? ' cf-flow__slot--idle' : ''}`}
      aria-label={label}
      title={label}
      aria-disabled={disabled}
      ref={(el) => {
        if (el) {
          el.style.left = `${point.x}px`;
          el.style.top = `${point.y}px`;
        }
      }}
      onClick={onClick}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      <IconPlus size={12} />
    </button>
  );
}
