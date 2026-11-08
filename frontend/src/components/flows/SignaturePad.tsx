/**
 * SignaturePad — the signature of a handover or return (AST-02, E-51,
 * WP-276, WP-807): a canvas drawn with mouse, pen or finger, a "Clear"
 * button and a hint for screen readers. The result is the PNG of the canvas
 * in base64 (without the data: prefix), as SignatureInput.image_png_base64
 * of POST /assets/{id}/assign|return|transfer expects it. The server checks,
 * stores and hashes the image (WP-276); the pad only draws it.
 */
import { useId, useRef } from 'react';
import type { PointerEvent as ReactPointerEvent, TouchEvent as ReactTouchEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '../ui/Button';
import { labelClasses } from '../ui/Input';

/** SIGNATURE_MAX_PNG_BYTES is the decoded size of the PNG the server takes (WP-276, 413 above). */
export const SIGNATURE_MAX_PNG_BYTES = 512 * 1024;
/**
 * REQUEST_MAX_BYTES is the body limit of the proxy in front of the API:
 * frontend/nginx.conf sets no client_max_body_size, so the nginx default of
 * 1 MiB applies to every request.
 */
export const REQUEST_MAX_BYTES = 1024 * 1024;

/** base64Bytes is the decoded size of a base64 string. */
export function base64Bytes(base64: string): number {
  const padding = base64.endsWith('==') ? 2 : base64.endsWith('=') ? 1 : 0;
  return Math.floor((base64.length * 3) / 4) - padding;
}

/**
 * signatureTooLarge tells whether a request with a signature must not be
 * sent: the PNG above the limit of the server or the JSON body above the
 * limit of the proxy. Nothing is sent then; the dialog says so.
 */
export function signatureTooLarge(png: string, body: unknown): boolean {
  if (base64Bytes(png) > SIGNATURE_MAX_PNG_BYTES) return true;
  return new TextEncoder().encode(JSON.stringify(body)).length > REQUEST_MAX_BYTES;
}

const CANVAS_WIDTH = 480;
const CANVAS_HEIGHT = 160;
const PNG_PREFIX = 'data:image/png;base64,';

export interface SignaturePadProps {
  label: string;
  /** value is the PNG in base64, null while the pad is empty. */
  value: string | null;
  onChange: (png: string | null) => void;
}

interface Point {
  x: number;
  y: number;
}

export function SignaturePad({ label, value, onChange }: SignaturePadProps) {
  const { t } = useTranslation();
  const hintId = useId();
  const labelId = useId();
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const lastRef = useRef<Point | null>(null);
  const drawnRef = useRef(false);

  // The canvas has its own pixel size; a point of the event is scaled to it.
  const pointOf = (clientX: number, clientY: number): Point | null => {
    const canvas = canvasRef.current;
    if (!canvas) return null;
    const rect = canvas.getBoundingClientRect();
    const scaleX = rect.width > 0 ? canvas.width / rect.width : 1;
    const scaleY = rect.height > 0 ? canvas.height / rect.height : 1;
    return { x: (clientX - rect.left) * scaleX, y: (clientY - rect.top) * scaleY };
  };

  const context = () => {
    const ctx = canvasRef.current?.getContext('2d');
    if (!ctx) return null;
    ctx.lineWidth = 2.5;
    ctx.lineCap = 'round';
    ctx.lineJoin = 'round';
    // Ink on paper: the pad stays white in both themes, like the signed sheet.
    ctx.strokeStyle = 'black';
    return ctx;
  };

  const start = (clientX: number, clientY: number) => {
    const point = pointOf(clientX, clientY);
    if (!point) return;
    lastRef.current = point;
    const ctx = context();
    if (!ctx) return;
    ctx.beginPath();
    ctx.arc(point.x, point.y, ctx.lineWidth / 2, 0, Math.PI * 2);
    ctx.fillStyle = ctx.strokeStyle;
    ctx.fill();
    drawnRef.current = true;
  };

  const move = (clientX: number, clientY: number) => {
    const last = lastRef.current;
    const point = pointOf(clientX, clientY);
    if (!last || !point) return;
    const ctx = context();
    if (ctx) {
      ctx.beginPath();
      ctx.moveTo(last.x, last.y);
      ctx.lineTo(point.x, point.y);
      ctx.stroke();
      drawnRef.current = true;
    }
    lastRef.current = point;
  };

  // A finished stroke hands the whole canvas on as PNG.
  const end = () => {
    if (!lastRef.current) return;
    lastRef.current = null;
    const canvas = canvasRef.current;
    if (!canvas || !drawnRef.current) return;
    const url = canvas.toDataURL('image/png');
    onChange(url.startsWith(PNG_PREFIX) ? url.slice(PNG_PREFIX.length) : null);
  };

  const clear = () => {
    const canvas = canvasRef.current;
    canvas?.getContext('2d')?.clearRect(0, 0, canvas.width, canvas.height);
    drawnRef.current = false;
    lastRef.current = null;
    onChange(null);
  };

  const pointer = {
    onPointerDown: (event: ReactPointerEvent<HTMLCanvasElement>) => {
      event.currentTarget.setPointerCapture?.(event.pointerId);
      start(event.clientX, event.clientY);
    },
    onPointerMove: (event: ReactPointerEvent<HTMLCanvasElement>) =>
      move(event.clientX, event.clientY),
    onPointerUp: end,
    onPointerCancel: end,
    onPointerLeave: end,
  };
  // Browsers without pointer events report the finger as touch events.
  const touch =
    typeof window !== 'undefined' && !('PointerEvent' in window)
      ? {
          onTouchStart: (event: ReactTouchEvent<HTMLCanvasElement>) => {
            const first = event.touches[0];
            if (first) start(first.clientX, first.clientY);
          },
          onTouchMove: (event: ReactTouchEvent<HTMLCanvasElement>) => {
            const first = event.touches[0];
            if (first) move(first.clientX, first.clientY);
          },
          onTouchEnd: end,
          onTouchCancel: end,
        }
      : {};

  return (
    <div className="space-y-1.5">
      <span id={labelId} className={labelClasses}>
        {label}
      </span>
      <canvas
        ref={canvasRef}
        width={CANVAS_WIDTH}
        height={CANVAS_HEIGHT}
        role="img"
        aria-labelledby={labelId}
        aria-describedby={hintId}
        data-signed={value ? 'true' : 'false'}
        className="block h-40 w-full cursor-crosshair touch-none rounded-lg border border-control bg-white"
        {...pointer}
        {...touch}
      />
      <div className="flex items-start justify-between gap-2">
        <p id={hintId} className="text-xs text-fg-muted">
          {value ? t('signature.signed') : t('signature.hint')}
        </p>
        <Button variant="secondary" size="sm" onClick={clear} disabled={!value}>
          {t('signature.clear')}
        </Button>
      </div>
    </div>
  );
}
