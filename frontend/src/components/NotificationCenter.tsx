/**
 * NotificationCenter — the bell of the layout (UI-16, NTF-04): the number
 * of unread notifications and a panel with the latest ones; each can be
 * marked as read, or all at once. A failed read is shown as such with a
 * retry, never as "no notifications"; while the unread count is unknown the
 * bell shows no number (F-348).
 */
import { Bell } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  useMarkAllNotificationsRead,
  useMarkNotificationRead,
  useNotifications,
} from '../hooks/useNotifications';
import { Button } from './ui/Button';
import { cn } from './ui/utils';

export function NotificationCenter() {
  const { t, i18n } = useTranslation();
  const [open, setOpen] = useState(false);
  const container = useRef<HTMLDivElement>(null);
  const unread = useNotifications(true);
  const all = useNotifications(false, open);
  const markRead = useMarkNotificationRead();
  const markAll = useMarkAllNotificationsRead();
  // undefined while the count is not known (loading or failed, also a
  // failed refresh of an earlier count).
  const unreadCount = unread.isError ? undefined : unread.data?.total;
  const items = all.data?.data ?? [];
  const failed = [unread, all].filter((query) => query.isError);

  // A click outside closes the panel like Escape does.
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (container.current && !container.current.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener('pointerdown', onPointerDown);
    return () => document.removeEventListener('pointerdown', onPointerDown);
  }, [open]);

  return (
    <div className="relative" ref={container}>
      <button
        type="button"
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={
          unreadCount === undefined
            ? t('notifications.openUnknown')
            : t('notifications.open', { count: unreadCount })
        }
        title={t('notifications.title')}
        onClick={() => setOpen((value) => !value)}
        className="relative inline-flex h-9 w-9 items-center justify-center rounded-lg text-fg-muted transition-colors hover:bg-fg/[0.06] hover:text-fg focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent aria-expanded:bg-fg/[0.06] aria-expanded:text-fg"
      >
        <Bell aria-hidden="true" className="h-4 w-4" />
        {unreadCount !== undefined && unreadCount > 0 ? (
          <span
            aria-hidden="true"
            className="absolute right-0.5 top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-accent px-1 text-[11px] font-semibold leading-none tabular-nums text-accent-fg ring-2 ring-canvas"
          >
            {unreadCount > 99 ? '99+' : unreadCount}
          </span>
        ) : null}
      </button>
      {open ? (
        <div
          role="dialog"
          aria-label={t('notifications.title')}
          className="absolute right-0 z-40 mt-2 w-[min(92vw,24rem)] animate-menu-in rounded-xl border border-line bg-raised p-3 shadow-lg"
          onKeyDown={(event) => {
            if (event.key === 'Escape') setOpen(false);
          }}
        >
          <div className="mb-2 flex items-center justify-between">
            <h2 className="text-sm font-semibold">{t('notifications.title')}</h2>
            <Button
              variant="ghost"
              size="sm"
              disabled={unreadCount === 0 || markAll.isPending}
              onClick={() => markAll.mutate()}
            >
              {t('notifications.markAllRead')}
            </Button>
          </div>
          {failed.length > 0 ? (
            <div role="alert" className="mb-2 text-sm text-danger">
              <p>{t('notifications.loadError')}</p>
              <Button
                variant="secondary"
                size="sm"
                className="mt-1"
                onClick={() => failed.forEach((query) => void query.refetch())}
              >
                {t('common.retry')}
              </Button>
            </div>
          ) : null}
          {all.isError ? null : all.isLoading ? (
            <p className="text-sm text-muted">{t('app.loading')}</p>
          ) : items.length === 0 ? (
            <p className="py-4 text-center text-sm text-muted">{t('notifications.empty')}</p>
          ) : (
            <ul className="scroll-thin max-h-96 space-y-1 overflow-y-auto">
              {items.map((item) => {
                const isUnread = !item.read_at;
                return (
                  <li
                    key={item.id}
                    className={cn('rounded-lg p-2.5 text-sm', isUnread && 'bg-accent-soft')}
                    data-unread={isUnread ? 'true' : undefined}
                  >
                    <p className="font-medium">
                      {isUnread ? (
                        <span className="sr-only">{t('notifications.unread')}: </span>
                      ) : null}
                      {item.subject}
                    </p>
                    {item.body ? <p className="mt-0.5 text-fg-muted">{item.body}</p> : null}
                    <div className="mt-1.5 flex items-center justify-between text-xs text-muted">
                      <time dateTime={item.created_at}>
                        {new Date(item.created_at).toLocaleString(i18n.language)}
                      </time>
                      {isUnread ? (
                        <button
                          type="button"
                          className="font-medium text-accent hover:underline"
                          onClick={() => markRead.mutate(item.id)}
                        >
                          {t('notifications.markRead')}
                        </button>
                      ) : null}
                    </div>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      ) : null}
    </div>
  );
}
