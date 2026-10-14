/**
 * NotificationCenter — the bell of the layout (UI-16, NTF-04): the number
 * of unread notifications and a panel with the latest ones; each can be
 * marked as read, or all at once.
 */
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  useMarkAllNotificationsRead,
  useMarkNotificationRead,
  useNotifications,
} from '../hooks/useNotifications';
import { Button } from './ui/Button';

export function NotificationCenter() {
  const { t, i18n } = useTranslation();
  const [open, setOpen] = useState(false);
  const unread = useNotifications(true);
  const all = useNotifications(false, open);
  const markRead = useMarkNotificationRead();
  const markAll = useMarkAllNotificationsRead();
  const unreadCount = unread.data?.total ?? 0;
  const items = all.data?.data ?? [];

  return (
    <div className="relative">
      <Button
        variant="secondary"
        size="sm"
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={t('notifications.open', { count: unreadCount })}
        onClick={() => setOpen((value) => !value)}
      >
        <svg
          viewBox="0 0 24 24"
          className="h-4 w-4"
          aria-hidden="true"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
        >
          <path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9" />
          <path d="M10.3 21a1.94 1.94 0 0 0 3.4 0" />
        </svg>
        {unreadCount > 0 ? (
          <span className="ml-1 rounded-full bg-red-600 px-1.5 text-xs font-semibold text-white">
            {unreadCount}
          </span>
        ) : null}
      </Button>
      {open ? (
        <div
          role="dialog"
          aria-label={t('notifications.title')}
          className="absolute right-0 z-40 mt-2 w-80 rounded-lg border border-gray-200 bg-white p-3 shadow-lg dark:border-gray-700 dark:bg-gray-900"
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
          {all.isLoading ? (
            <p className="text-sm text-gray-500">{t('app.loading')}</p>
          ) : items.length === 0 ? (
            <p className="text-sm text-gray-500">{t('notifications.empty')}</p>
          ) : (
            <ul className="max-h-96 space-y-2 overflow-y-auto">
              {items.map((item) => {
                const isUnread = !item.read_at;
                return (
                  <li
                    key={item.id}
                    className={`rounded-md p-2 text-sm ${isUnread ? 'bg-primary/10' : ''}`}
                    data-unread={isUnread ? 'true' : undefined}
                  >
                    <p className="font-medium">
                      {isUnread ? (
                        <span className="sr-only">{t('notifications.unread')}: </span>
                      ) : null}
                      {item.subject}
                    </p>
                    {item.body ? (
                      <p className="text-gray-600 dark:text-gray-300">{item.body}</p>
                    ) : null}
                    <div className="mt-1 flex items-center justify-between text-xs text-gray-500">
                      <time dateTime={item.created_at}>
                        {new Date(item.created_at).toLocaleString(i18n.language)}
                      </time>
                      {isUnread ? (
                        <button
                          type="button"
                          className="text-primary hover:underline"
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
