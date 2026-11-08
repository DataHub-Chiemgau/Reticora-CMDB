/**
 * ScanDialog — reading a code with the camera, a handheld scanner or by
 * typing it (INV-03, UI-15, E-52, WP-807). The camera runs only in a secure
 * context (HTTPS or localhost) through navigator.mediaDevices.getUserMedia,
 * the rear camera preferred; the browser's BarcodeDetector reads Code128
 * and QR, and without it @zxing/browser is loaded as a chunk of its own.
 * Without secure context, permission or camera the dialog says why and
 * offers the text field, which a handheld scanner fills like a keyboard
 * (Enter ends the code); the field works with the camera, too. Closing the
 * dialog ends the camera stream.
 *
 * A code is resolved by useScanLookup: an object URN locally through the
 * GET of its kind, any other code through GET /labels/lookup (WP-762).
 * Inside a flow (onPick) a hit of the accepted kind fills the picker and
 * closes the dialog. Outside a flow the dialog shows kind, name and code of
 * the hit and its actions: an asset is assigned, returned, reserved or
 * shown with its history, a consumable received, moved or shown with its
 * history, a location shows its name and path.
 *
 * Everything is offered only with the feature barcode (E-44) and the module
 * inventory, which serves /api/v1/labels (E-105, useScanGate).
 */
import { useEffect, useId, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { ScanLine } from 'lucide-react';
import { ApiError, assignmentApi, teamApi, userApi } from '../../api/client';
import type { LocationKind, ScanKind, ScannedObject } from '../../api/cmdb';
import { useScanLookup } from '../../api/cmdbHooks';
import { useEntitlements, useModules } from '../../hooks/useEntitlements';
import { ObjectHistory, useLocationNames } from '../cmdb/ObjectHistory';
import { accessErrorMessage, type PickedObject } from '../pickers/ObjectPicker';
import type { PickedLocation } from '../pickers/LocationPicker';
import { Button, IconButton } from '../ui/Button';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';
import { AssignDialog, ReturnDialog, type HeldAsset } from './AssignDialogs';
import {
  MoveStockDialog,
  ReceiveDialog,
  ReserveDialog,
  useStockFlowGates,
} from './StockFlowDialogs';

// ─── BarcodeDetector (Shape Detection API), declared here ───────────────────

interface DetectedBarcode {
  rawValue: string;
  format: string;
}

interface BarcodeDetectorLike {
  detect(source: HTMLVideoElement): Promise<DetectedBarcode[]>;
}

type BarcodeDetectorConstructor = new (options?: { formats?: string[] }) => BarcodeDetectorLike;

/** BARCODE_FORMATS are the symbologies of the labels (E-52): Code128 and QR. */
const BARCODE_FORMATS = ['code_128', 'qr_code'];
/** SCAN_INTERVAL_MS is the pause between two frames BarcodeDetector reads. */
const SCAN_INTERVAL_MS = 200;

function barcodeDetector(): BarcodeDetectorConstructor | undefined {
  return (window as unknown as { BarcodeDetector?: BarcodeDetectorConstructor }).BarcodeDetector;
}

// ─── Gate ────────────────────────────────────────────────────────────────────

/**
 * useScanGate tells whether scanning is offered: the feature barcode of the
 * plan (E-44) and the module inventory that serves /api/v1/labels (E-105).
 * Without it the pickers keep their search.
 */
export function useScanGate(): boolean {
  const { isEnabled } = useEntitlements();
  const { isModuleEnabled } = useModules();
  return isEnabled('barcode') && isModuleEnabled('inventory');
}

// ─── Camera ──────────────────────────────────────────────────────────────────

/** ManualReason says why the dialog reads codes from the text field only. */
type ManualReason = 'insecure' | 'denied' | 'noCamera' | 'error' | 'chosen';

type CameraState =
  { mode: 'starting' } | { mode: 'camera' } | { mode: 'manual'; reason: ManualReason };

function reasonOf(error: unknown): ManualReason {
  const name = error instanceof Error || error instanceof DOMException ? error.name : '';
  if (name === 'NotAllowedError' || name === 'SecurityError') return 'denied';
  if (name === 'NotFoundError' || name === 'OverconstrainedError') return 'noCamera';
  return 'error';
}

function stopStream(stream: MediaStream | undefined) {
  for (const track of stream?.getTracks() ?? []) track.stop();
}

/**
 * useCamera runs the camera into the video element while active and hands
 * every code read to onCode. It stops the stream when it turns inactive or
 * the dialog unmounts.
 */
function useCamera(
  active: boolean,
  manual: boolean,
  videoRef: React.RefObject<HTMLVideoElement | null>,
  onCode: (code: string) => void,
): CameraState {
  const [state, setState] = useState<CameraState>({ mode: 'starting' });
  const onCodeRef = useRef(onCode);
  onCodeRef.current = onCode;

  useEffect(() => {
    if (!active || manual) return undefined;
    const element = videoRef.current;
    let cancelled = false;
    let stream: MediaStream | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let controls: { stop: () => void } | undefined;
    const toManual = (reason: ManualReason) => {
      if (!cancelled) setState({ mode: 'manual', reason });
    };

    const run = async () => {
      if (!window.isSecureContext) return toManual('insecure');
      const media = navigator.mediaDevices;
      if (!media || typeof media.getUserMedia !== 'function') return toManual('noCamera');
      setState({ mode: 'starting' });
      try {
        stream = await media.getUserMedia({
          video: { facingMode: { ideal: 'environment' } },
          audio: false,
        });
      } catch (error) {
        return toManual(reasonOf(error));
      }
      const video = element;
      if (cancelled || !video) {
        stopStream(stream);
        return undefined;
      }
      video.srcObject = stream;
      try {
        await video.play();
      } catch {
        // The preview may not play (autoplay rules); the frames still decode.
      }
      if (cancelled) return undefined;
      setState({ mode: 'camera' });

      const Detector = barcodeDetector();
      if (Detector) {
        const detector = new Detector({ formats: BARCODE_FORMATS });
        const tick = async () => {
          if (cancelled) return;
          try {
            const value = (await detector.detect(video))[0]?.rawValue;
            if (value && !cancelled) onCodeRef.current(value);
          } catch {
            // A frame that cannot be read is skipped.
          }
          if (!cancelled) timer = setTimeout(() => void tick(), SCAN_INTERVAL_MS);
        };
        void tick();
        return undefined;
      }
      try {
        // Without BarcodeDetector @zxing/browser decodes the stream; it is a
        // chunk of its own and loaded only here.
        const { BrowserMultiFormatReader } = await import('@zxing/browser');
        if (cancelled) return undefined;
        const reader = new BrowserMultiFormatReader();
        const started = await reader.decodeFromStream(stream, video, (result) => {
          if (result && !cancelled) onCodeRef.current(result.getText());
        });
        if (cancelled) started.stop();
        else controls = started;
      } catch {
        stopStream(stream);
        toManual('error');
      }
      return undefined;
    };
    void run();

    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
      controls?.stop();
      stopStream(stream);
      const video = element;
      if (video) video.srcObject = null;
    };
  }, [active, manual, videoRef]);

  if (manual) return { mode: 'manual', reason: 'chosen' };
  return state;
}

// ─── Scanner (camera and text field) ─────────────────────────────────────────

interface CodeScannerProps {
  /** active runs the camera; a shown hit pauses it. */
  active: boolean;
  onCode: (code: string) => void;
  busy: boolean;
}

function CodeScanner({ active, onCode, busy }: CodeScannerProps) {
  const { t } = useTranslation();
  const videoRef = useRef<HTMLVideoElement>(null);
  const statusId = useId();
  const [manual, setManual] = useState(false);
  const [code, setCode] = useState('');
  // The camera reads a code many times a second: the same code counts once.
  const lastRef = useRef<string | undefined>(undefined);
  // "Scan again" takes the same code once more.
  useEffect(() => {
    if (active) lastRef.current = undefined;
  }, [active]);
  const camera = useCamera(active, manual, videoRef, (value) => {
    if (value === lastRef.current) return;
    lastRef.current = value;
    onCode(value);
  });
  const showVideo = active && camera.mode !== 'manual';

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const value = code.trim();
    if (!value || busy) return;
    lastRef.current = undefined;
    onCode(value);
    setCode('');
  };

  let status: string;
  if (camera.mode === 'manual') status = t(`scan.reasons.${camera.reason}`);
  else if (camera.mode === 'starting') status = t('scan.starting');
  else status = t('scan.scanning');

  return (
    <div className="space-y-3">
      <video
        ref={videoRef}
        muted
        playsInline
        aria-label={t('scan.camera')}
        className={showVideo ? 'aspect-video w-full rounded-lg bg-black object-cover' : 'hidden'}
      />
      <div className="flex items-start justify-between gap-2">
        <p id={statusId} role="status" className="text-sm text-fg-muted">
          {status}
        </p>
        {active && camera.mode === 'camera' ? (
          <Button variant="secondary" size="sm" onClick={() => setManual(true)}>
            {t('scan.manualMode')}
          </Button>
        ) : null}
        {active && camera.mode === 'manual' && camera.reason === 'chosen' ? (
          <Button variant="secondary" size="sm" onClick={() => setManual(false)}>
            {t('scan.useCamera')}
          </Button>
        ) : null}
      </div>
      <form onSubmit={submit} className="flex items-end gap-2">
        <div className="min-w-0 flex-1">
          <Input
            label={t('scan.codeLabel')}
            value={code}
            onChange={(event) => setCode(event.target.value)}
            autoComplete="off"
            spellCheck={false}
            aria-describedby={statusId}
            autoFocus={camera.mode === 'manual'}
          />
        </div>
        <Button type="submit" disabled={!code.trim() || busy}>
          {t('scan.resolve')}
        </Button>
      </form>
    </div>
  );
}

// ─── Dialog ──────────────────────────────────────────────────────────────────

export interface ScanDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /**
   * onPick receives a hit inside a flow; the dialog then closes. Without it
   * the dialog shows the hit with its actions.
   */
  onPick?: (object: ScannedObject) => void;
  /** accept are the kinds the picker of the flow takes. */
  accept?: readonly ScanKind[];
  /** locationKinds limit the locations a flow takes, e.g. bins. */
  locationKinds?: readonly LocationKind[];
}

function accepts(
  object: ScannedObject,
  accept: readonly ScanKind[] | undefined,
  locationKinds: readonly LocationKind[] | undefined,
): boolean {
  if (accept && !accept.includes(object.kind)) return false;
  if (object.kind !== 'location' || !locationKinds) return true;
  return !!object.location && locationKinds.includes(object.location.kind);
}

/** notFound tells an unknown code: the lookup and the GET of a URN answer 404. */
function notFound(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404;
}

type Flow = 'assign' | 'return' | 'reserve' | 'receive' | 'move';

export function ScanDialog({ open, onOpenChange, onPick, accept, locationKinds }: ScanDialogProps) {
  const { t } = useTranslation();
  const lookup = useScanLookup();
  const [hit, setHit] = useState<{ object: ScannedObject; code: string } | null>(null);
  const [message, setMessage] = useState<string>();
  const [flow, setFlow] = useState<{ kind: Flow; object: PickedObject; held?: HeldAsset } | null>(
    null,
  );

  const close = (next: boolean) => {
    if (!next) {
      setHit(null);
      setMessage(undefined);
      lookup.reset();
    }
    onOpenChange(next);
  };

  const resolve = (code: string) => {
    if (lookup.isPending) return;
    setHit(null);
    setMessage(undefined);
    lookup.mutate(code, {
      onSuccess: (object) => {
        if (!onPick) {
          setHit({ object, code });
          return;
        }
        if (accepts(object, accept, locationKinds)) {
          onPick(object);
          close(false);
          return;
        }
        setMessage(t('scan.wrongKind', { kind: t(`scan.kinds.${object.kind}`) }));
      },
      onError: (error) =>
        setMessage(notFound(error) ? t('scan.notFound', { code }) : accessErrorMessage(error, t)),
    });
  };

  const startFlow = (kind: Flow, object: PickedObject, held?: HeldAsset) => {
    setFlow({ kind, object, held });
    close(false);
  };
  const endFlow = (next: boolean) => {
    if (!next) setFlow(null);
  };

  return (
    <>
      <Modal
        open={open}
        onOpenChange={close}
        title={t('scan.title')}
        description={t('scan.description')}
      >
        <div className="space-y-4">
          {open ? <CodeScanner active={!hit} onCode={resolve} busy={lookup.isPending} /> : null}
          {lookup.isPending ? (
            <p role="status" className="text-sm text-fg-muted">
              {t('scan.resolving')}
            </p>
          ) : null}
          {message ? (
            <p role="alert" className="text-sm text-danger">
              {message}
            </p>
          ) : null}
          {hit ? (
            <ScanResult
              object={hit.object}
              code={hit.code}
              onFlow={startFlow}
              onScanAgain={() => setHit(null)}
            />
          ) : null}
          <div className="flex justify-end">
            <Button variant="secondary" onClick={() => close(false)}>
              {t('scan.close')}
            </Button>
          </div>
        </div>
      </Modal>
      {onPick ? null : (
        <>
          <AssignDialog
            open={flow?.kind === 'assign'}
            onOpenChange={endFlow}
            initialAsset={flow?.kind === 'assign' ? flow.object : undefined}
          />
          <ReturnDialog
            open={flow?.kind === 'return'}
            onOpenChange={endFlow}
            target={flow?.kind === 'return' ? (flow.held ?? null) : null}
          />
          <ReserveDialog
            open={flow?.kind === 'reserve'}
            onOpenChange={endFlow}
            initialAsset={flow?.kind === 'reserve' ? flow.object : undefined}
          />
          <ReceiveDialog
            open={flow?.kind === 'receive'}
            onOpenChange={endFlow}
            initialItem={flow?.kind === 'receive' ? flow.object : undefined}
          />
          <MoveStockDialog
            open={flow?.kind === 'move'}
            onOpenChange={endFlow}
            initialItem={flow?.kind === 'move' ? flow.object : undefined}
          />
        </>
      )}
    </>
  );
}

// ─── Hit outside a flow ──────────────────────────────────────────────────────

const NAME_STALE_MS = 5 * 60_000;

/**
 * useHeldAsset reads the open assignment of an asset (GET
 * /assignments?asset_id=) and names its holder: the target of a return.
 */
function useHeldAsset(object: ScannedObject, label: string, enabled: boolean) {
  const { t } = useTranslation();
  const params = { asset_id: object.id, limit: 5 };
  const assignments = useQuery({
    queryKey: ['assignments', params],
    queryFn: () => assignmentApi.list(params),
    enabled: enabled && object.kind === 'asset',
    retry: false,
  });
  const open = (assignments.data?.data ?? []).find(
    (row) =>
      (row.status === 'active' || row.status === 'overdue') &&
      (row.assigned_to || row.assigned_team_id),
  );
  const teamId = open?.assigned_team_id;
  const userId = teamId ? undefined : open?.assigned_to;
  const user = useQuery({
    queryKey: ['user', userId],
    queryFn: () => userApi.get(userId!),
    enabled: !!userId,
    retry: false,
    staleTime: NAME_STALE_MS,
  });
  const team = useQuery({
    queryKey: ['team', teamId],
    queryFn: () => teamApi.get(teamId!),
    enabled: !!teamId,
    retry: false,
    staleTime: NAME_STALE_MS,
  });
  if (!open) return { held: undefined, loading: assignments.isLoading };
  const holder = (teamId ?? userId)!;
  const holderLabel = teamId
    ? t('assignment.teamHolder', { name: team.data?.name ?? teamId })
    : (user.data?.display_name ?? holder);
  const held: HeldAsset = { assetId: object.id, assetLabel: label, holder, holderLabel };
  return { held, loading: false };
}

interface ScanResultProps {
  object: ScannedObject;
  code: string;
  onFlow: (kind: Flow, object: PickedObject, held?: HeldAsset) => void;
  onScanAgain: () => void;
}

function ScanResult({ object, code, onFlow, onScanAgain }: ScanResultProps) {
  const { t } = useTranslation();
  const { isModuleEnabled } = useModules();
  const { isEnabled } = useEntitlements();
  const gates = useStockFlowGates();
  const [history, setHistory] = useState(false);
  const names = useLocationNames(object.kind === 'location');
  const canAssign = isModuleEnabled('inventory') && isEnabled('assets_basic');
  const shownCode = object.code ?? code;
  const label = object.code ? `${object.name} (${object.code})` : object.name;
  const { held, loading } = useHeldAsset(object, label, canAssign);
  const picked: PickedObject | undefined =
    object.kind === 'location'
      ? undefined
      : { kind: object.kind, id: object.id, name: object.name, code: object.code };

  return (
    <section aria-label={t('scan.result')} className="space-y-3 rounded-lg border border-line p-3">
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
        <dt className="text-fg-muted">{t('scan.kind')}</dt>
        <dd className="text-fg">{t(`scan.kindLabels.${object.kind}`)}</dd>
        <dt className="text-fg-muted">{t('scan.name')}</dt>
        <dd className="font-medium text-fg">{object.name}</dd>
        {object.kind === 'location' ? (
          <>
            <dt className="text-fg-muted">{t('scan.path')}</dt>
            <dd className="text-fg">{names.path(object.id)}</dd>
          </>
        ) : (
          <>
            <dt className="text-fg-muted">
              {object.kind === 'asset' ? t('scan.inventoryNo') : t('scan.code')}
            </dt>
            <dd className="font-mono text-fg">{shownCode}</dd>
          </>
        )}
      </dl>
      {picked ? (
        <div className="flex flex-wrap gap-2">
          {object.kind === 'asset' ? (
            <>
              {canAssign ? (
                <Button size="sm" onClick={() => onFlow('assign', picked)}>
                  {t('scan.actions.assign')}
                </Button>
              ) : null}
              {canAssign ? (
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={!held}
                  loading={loading}
                  onClick={() => held && onFlow('return', picked, held)}
                >
                  {t('scan.actions.return')}
                </Button>
              ) : null}
              {gates.reserve ? (
                <Button size="sm" variant="secondary" onClick={() => onFlow('reserve', picked)}>
                  {t('scan.actions.reserve')}
                </Button>
              ) : null}
            </>
          ) : (
            <>
              {gates.receive ? (
                <Button size="sm" onClick={() => onFlow('receive', picked)}>
                  {t('scan.actions.receive')}
                </Button>
              ) : null}
              {gates.move ? (
                <Button size="sm" variant="secondary" onClick={() => onFlow('move', picked)}>
                  {t('scan.actions.move')}
                </Button>
              ) : null}
            </>
          )}
          <Button
            size="sm"
            variant="ghost"
            aria-expanded={history}
            onClick={() => setHistory((shown) => !shown)}
          >
            {t('scan.actions.history')}
          </Button>
        </div>
      ) : null}
      {canAssign && object.kind === 'asset' && !loading && !held ? (
        <p className="text-xs text-fg-muted">{t('scan.notAssigned')}</p>
      ) : null}
      {history && object.kind !== 'location' ? (
        <ObjectHistory entityType={object.kind} id={object.id} />
      ) : null}
      <div className="flex justify-end">
        <Button size="sm" variant="ghost" onClick={onScanAgain}>
          {t('scan.scanAgain')}
        </Button>
      </div>
    </section>
  );
}

// ─── Scan button of a picker ─────────────────────────────────────────────────

export interface ScanButtonProps {
  /** field names the picker the hit goes to, e.g. "Asset". */
  field: string;
  accept: readonly ScanKind[];
  locationKinds?: readonly LocationKind[];
  /** onObject receives a hit of the kinds asset and consumable. */
  onObject?: (object: PickedObject) => void;
  /** onLocation receives a hit of the kind location. */
  onLocation?: (location: PickedLocation) => void;
}

/**
 * ScanButton sits next to an ObjectPicker or LocationPicker of a flow: a
 * hit of the accepted kind goes straight into the picker. It is offered
 * only with useScanGate; without it the picker keeps its search.
 */
export function ScanButton({
  field,
  accept,
  locationKinds,
  onObject,
  onLocation,
}: ScanButtonProps) {
  const { t } = useTranslation();
  const allowed = useScanGate();
  const [open, setOpen] = useState(false);
  const wantsLocation = accept.includes('location');
  const names = useLocationNames(open && wantsLocation);
  if (!allowed) return null;

  const pick = (object: ScannedObject) => {
    if (object.kind === 'location') {
      const path = names.path(object.id);
      onLocation?.({
        id: object.id,
        name: object.name,
        kind: object.location?.kind ?? 'bin',
        path: path && path !== object.id ? path.split(' › ') : [object.name],
      });
      return;
    }
    onObject?.({ kind: object.kind, id: object.id, name: object.name, code: object.code });
  };

  return (
    <>
      <IconButton
        variant="secondary"
        icon={<ScanLine className="h-4 w-4" />}
        label={t('scan.scanFor', { field })}
        onClick={() => setOpen(true)}
      />
      <ScanDialog
        open={open}
        onOpenChange={setOpen}
        onPick={pick}
        accept={accept}
        locationKinds={locationKinds}
      />
    </>
  );
}

/**
 * WithScan lays a picker and its scan button side by side; without
 * useScanGate the picker stands alone.
 */
export function WithScan({ children, scan }: { children: React.ReactNode; scan: React.ReactNode }) {
  const allowed = useScanGate();
  // The same elements either way: the picker keeps its state when the gate
  // turns once the plan has loaded.
  return (
    <div className="flex items-start gap-2">
      <div className="min-w-0 flex-1">{children}</div>
      {allowed ? <div className="pt-7">{scan}</div> : null}
    </div>
  );
}
