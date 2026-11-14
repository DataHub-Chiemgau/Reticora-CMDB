/**
 * SignatureField is the control of a form field of type signature (WFL-01,
 * WP-1101, WP-1133): a drawing surface for pointer, pen and touch with a
 * reset. A finished stroke hands the picture as a PNG Blob to onChange; the
 * form renderer uploads it as a document on submission (uploadSignatures)
 * and sends the document id as the value. A stored value (a document id)
 * is shown as a captured signature; reset clears it.
 */
import { useId, useRef, useState, type PointerEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '../ui/Button';

export interface SignatureFieldProps {
  label: string;
  /** A PNG Blob of a new signature, the document id of a stored one, or nothing. */
  value: unknown;
  onChange: (value: Blob | undefined) => void;
  readOnly?: boolean;
  /** required is shown in the label (DynamicForm adds the asterisk). */
  required?: boolean;
  error?: string;
}

const WIDTH = 480;
const HEIGHT = 160;

export function SignatureField({
  label,
  value,
  onChange,
  readOnly = false,
  error,
}: SignatureFieldProps) {
  const { t } = useTranslation();
  const id = useId();
  const canvas = useRef<HTMLCanvasElement | null>(null);
  const drawing = useRef(false);
  const [drawn, setDrawn] = useState(false);
  const captured = drawn || value instanceof Blob || (typeof value === 'string' && value !== '');

  const point = (event: PointerEvent<HTMLCanvasElement>) => {
    const el = event.currentTarget;
    const box = el.getBoundingClientRect();
    const sx = box.width ? WIDTH / box.width : 1;
    const sy = box.height ? HEIGHT / box.height : 1;
    return { x: (event.clientX - box.left) * sx, y: (event.clientY - box.top) * sy };
  };

  const start = (event: PointerEvent<HTMLCanvasElement>) => {
    if (readOnly) return;
    const ctx = event.currentTarget.getContext('2d');
    drawing.current = true;
    event.currentTarget.setPointerCapture?.(event.pointerId);
    if (!ctx) return;
    const p = point(event);
    ctx.lineWidth = 2;
    ctx.lineCap = 'round';
    // The ink is the text colour of the pad (text-gray-900 on white).
    ctx.strokeStyle = getComputedStyle(event.currentTarget).color;
    ctx.beginPath();
    ctx.moveTo(p.x, p.y);
  };

  const move = (event: PointerEvent<HTMLCanvasElement>) => {
    if (!drawing.current) return;
    const ctx = event.currentTarget.getContext('2d');
    if (!ctx) return;
    const p = point(event);
    ctx.lineTo(p.x, p.y);
    ctx.stroke();
  };

  const end = (event: PointerEvent<HTMLCanvasElement>) => {
    if (!drawing.current) return;
    drawing.current = false;
    setDrawn(true);
    event.currentTarget.toBlob((blob) => {
      if (blob) onChange(blob);
    }, 'image/png');
  };

  const reset = () => {
    const el = canvas.current;
    el?.getContext('2d')?.clearRect(0, 0, WIDTH, HEIGHT);
    setDrawn(false);
    onChange(undefined);
  };

  return (
    <div className="space-y-1.5">
      <span id={`${id}-label`} className="block text-sm font-medium text-fg">
        {label}
      </span>
      <canvas
        ref={canvas}
        width={WIDTH}
        height={HEIGHT}
        role="img"
        aria-labelledby={`${id}-label`}
        aria-describedby={`${id}-status`}
        data-testid="signature-pad"
        className="h-40 w-full max-w-[30rem] touch-none rounded-lg border border-line bg-white text-gray-900"
        onPointerDown={start}
        onPointerMove={move}
        onPointerUp={end}
        onPointerLeave={end}
      />
      <div className="flex flex-wrap items-center gap-3">
        <span id={`${id}-status`} className="text-xs text-fg-muted">
          {captured ? t('forms.signature.captured') : t('forms.signature.hint')}
        </span>
        {!readOnly && (
          <Button type="button" size="sm" variant="ghost" onClick={reset} disabled={!captured}>
            {t('forms.signature.reset')}
          </Button>
        )}
      </div>
      {error ? (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      ) : null}
    </div>
  );
}
