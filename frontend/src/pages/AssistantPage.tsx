import { MessageSquarePlus, SendHorizontal, Sparkles } from 'lucide-react';
import { FormEvent, KeyboardEvent, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAIConversations, useAskAI } from '../api/hooks';
import type { AICitation } from '../api/client';
import { Button, IconButton } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { PageHeader } from '../components/ui/PageHeader';
import { Skeleton, SkeletonList } from '../components/ui/Skeleton';
import { Textarea } from '../components/ui/Textarea';
import { cn } from '../components/ui/utils';

interface Message {
  id: number;
  role: 'user' | 'assistant';
  text: string;
  citations?: AICitation[];
}

export function AssistantPage() {
  const { t } = useTranslation();
  const conversations = useAIConversations();
  const ask = useAskAI();
  const [question, setQuestion] = useState('');
  const [conversationId, setConversationId] = useState<string | undefined>();
  // The thread of this visit: the API returns the answer to one question
  // at a time, so the page keeps the exchange itself.
  const [messages, setMessages] = useState<Message[]>([]);
  const log = useRef<HTMLDivElement>(null);
  const nextId = useRef(0);

  useEffect(() => {
    const element = log.current;
    if (element) element.scrollTop = element.scrollHeight;
  }, [messages, ask.isPending]);

  function submit(event?: FormEvent) {
    event?.preventDefault();
    const trimmed = question.trim();
    if (!trimmed || ask.isPending) return;
    ask.mutate(
      { question: trimmed, conversation_id: conversationId },
      {
        onSuccess: (data) => {
          setConversationId(data.conversation_id);
          setMessages((current) => [
            ...current,
            { id: nextId.current++, role: 'user', text: trimmed },
            {
              id: nextId.current++,
              role: 'assistant',
              text: data.answer,
              citations: data.citations,
            },
          ]);
          setQuestion('');
        },
      },
    );
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault();
      submit();
    }
  }

  function select(id: string | undefined) {
    if (id === conversationId) return;
    // The thread on screen belongs to the conversation it was asked in.
    setConversationId(id);
    setMessages([]);
    ask.reset();
  }

  const selectedTitle = conversations.data?.find((entry) => entry.id === conversationId)?.title;
  const pendingQuestion = ask.isPending ? ask.variables?.question : undefined;

  return (
    <div className="space-y-4">
      <PageHeader title={t('assistant.title')} description={t('assistant.subtitle')} />
      <div className="grid gap-4 lg:h-[calc(100dvh-14rem)] lg:min-h-[30rem] lg:grid-cols-[16rem_minmax(0,1fr)]">
        <Card className="flex max-h-56 min-h-0 flex-col overflow-hidden p-0 lg:max-h-none">
          <div className="flex items-center justify-between gap-2 border-b border-line py-2 pl-4 pr-2">
            <h2 className="text-base font-semibold text-fg">{t('assistant.conversations')}</h2>
            <IconButton
              label={t('assistant.newConversation')}
              icon={<MessageSquarePlus />}
              size="sm"
              onClick={() => select(undefined)}
            />
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto p-2">
            {conversations.isLoading ? (
              <SkeletonList rows={4} label={t('assistant.loading')} />
            ) : conversations.isError ? (
              <ErrorState
                title={t('assistant.errorTitle')}
                description={conversations.error.message}
                retryLabel={t('common.retry')}
                onRetry={() => conversations.refetch()}
              />
            ) : conversations.data?.length ? (
              <ul className="space-y-0.5">
                {conversations.data.map((conversation) => (
                  <li key={conversation.id}>
                    <button
                      type="button"
                      aria-current={conversation.id === conversationId ? 'true' : undefined}
                      className={cn(
                        'block w-full truncate rounded-lg px-3 py-2 text-left text-sm text-fg-muted transition-colors hover:bg-fg/[0.05] hover:text-fg focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent',
                        conversation.id === conversationId &&
                          'bg-accent-soft font-medium text-fg hover:bg-accent-soft',
                      )}
                      onClick={() => select(conversation.id)}
                    >
                      {conversation.title}
                    </button>
                  </li>
                ))}
              </ul>
            ) : (
              <div className="px-2 py-4">
                <p className="text-sm font-medium text-fg">{t('assistant.empty')}</p>
                <p className="mt-1 text-sm text-fg-muted">{t('assistant.emptyHint')}</p>
              </div>
            )}
          </div>
        </Card>

        <Card className="flex h-[32rem] min-h-0 min-w-0 flex-col overflow-hidden p-0 lg:h-auto">
          <div className="border-b border-line px-5 py-3">
            <h2 className="truncate text-base font-semibold text-fg">
              {selectedTitle ?? t('assistant.askTitle')}
            </h2>
          </div>

          <div
            ref={log}
            role="log"
            aria-label={t('assistant.title')}
            // The message list scrolls: it must be reachable with the keyboard.
            tabIndex={0}
            className="min-h-0 flex-1 space-y-4 overflow-y-auto bg-canvas/60 p-5 focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent"
          >
            {messages.length === 0 && !pendingQuestion ? (
              <div className="flex h-full flex-col items-center justify-center gap-1.5 text-center">
                <span
                  aria-hidden="true"
                  className="mb-2 inline-flex h-10 w-10 items-center justify-center rounded-lg border border-line bg-panel text-fg-muted"
                >
                  <Sparkles className="h-5 w-5" />
                </span>
                <p className="text-base font-semibold text-fg">{t('assistant.askTitle')}</p>
              </div>
            ) : null}

            {messages.map((message) => (
              <Bubble key={message.id} message={message} />
            ))}
            {pendingQuestion ? (
              <>
                <Bubble message={{ id: -1, role: 'user', text: pendingQuestion }} />
                <div
                  role="status"
                  className="max-w-[85%] space-y-2 rounded-xl border border-line bg-panel px-4 py-3"
                >
                  <span className="sr-only">{t('assistant.thinking')}</span>
                  <Skeleton className="h-3 w-56" />
                  <Skeleton className="h-3 w-40" />
                </div>
              </>
            ) : null}
            {ask.isError ? <ErrorState title={ask.error.message} /> : null}
          </div>

          <form onSubmit={submit} className="border-t border-line bg-panel p-3">
            <div className="flex items-end gap-2">
              <div className="min-w-0 flex-1">
                <Textarea
                  aria-label={t('assistant.questionLabel')}
                  aria-describedby="assistant-hint"
                  rows={2}
                  className="max-h-40 min-h-[3rem] resize-none"
                  value={question}
                  onChange={(event) => setQuestion(event.target.value)}
                  onKeyDown={onKeyDown}
                  placeholder={t('assistant.placeholder')}
                />
              </div>
              <Button
                type="submit"
                icon={<SendHorizontal />}
                loading={ask.isPending}
                disabled={!question.trim()}
                className="mb-px h-12"
              >
                {t('assistant.ask')}
              </Button>
            </div>
            <p id="assistant-hint" className="mt-1.5 text-xs text-fg-muted">
              {t('assistant.sendHint')}
            </p>
          </form>
        </Card>
      </div>
    </div>
  );
}

/** Bubble is one message: the user's question at the right, the answer at the left. */
function Bubble({ message }: { message: Message }) {
  const { t } = useTranslation();
  if (message.role === 'user') {
    return (
      <div className="ml-auto w-fit max-w-[85%] whitespace-pre-wrap rounded-xl bg-accent px-4 py-2.5 text-sm text-accent-fg [overflow-wrap:anywhere]">
        <span className="sr-only">{t('assistant.questionLabel')}: </span>
        {message.text}
      </div>
    );
  }
  return (
    <div className="w-fit max-w-[85%] rounded-xl border border-line bg-panel px-4 py-3">
      <p className="whitespace-pre-wrap text-sm leading-6 text-fg [overflow-wrap:anywhere]">
        <span className="sr-only">{t('assistant.answer')}: </span>
        {message.text}
      </p>
      {message.citations?.length ? (
        <div className="mt-3 border-t border-line pt-2.5">
          <p className="text-xs font-medium text-fg-muted">{t('assistant.citations')}</p>
          <ul className="mt-1.5 space-y-1">
            {message.citations.map((citation) => (
              <li key={`${citation.entity_type}-${citation.entity_id}`} className="text-sm">
                <a
                  className="rounded text-accent underline-offset-2 hover:underline"
                  href={citation.url}
                >
                  {citation.title}
                </a>{' '}
                <span className="font-mono text-xs text-fg-muted">({citation.entity_type})</span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
