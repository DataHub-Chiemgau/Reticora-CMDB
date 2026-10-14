import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { fetchAPI, type PaginatedResponse } from '../api/client';

/** An in-app notification of the signed-in user (NTF-04). */
export interface InAppNotification {
  id: string;
  category: string;
  subject: string;
  body: string;
  data?: Record<string, unknown>;
  created_at: string;
  read_at?: string;
}

const KEY = ['notifications'];

/** useNotifications lists the user's notifications, newest first. */
export function useNotifications(unreadOnly = false, enabled = true) {
  return useQuery({
    queryKey: [...KEY, unreadOnly],
    queryFn: () =>
      fetchAPI<PaginatedResponse<InAppNotification>>(
        `/notifications?limit=50${unreadOnly ? '&unread=true' : ''}`,
      ),
    enabled,
    refetchInterval: 60_000,
  });
}

export function useMarkNotificationRead() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => fetchAPI<void>(`/notifications/${id}/read`, { method: 'POST' }),
    onSettled: () => queryClient.invalidateQueries({ queryKey: KEY }),
  });
}

export function useMarkAllNotificationsRead() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => fetchAPI<{ marked: number }>('/notifications/read-all', { method: 'POST' }),
    onSettled: () => queryClient.invalidateQueries({ queryKey: KEY }),
  });
}
