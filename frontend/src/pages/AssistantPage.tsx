import { FormEvent, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAIConversations, useAskAI } from '../api/hooks';
import type { AICitation } from '../api/client';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { EmptyState } from '../components/ui/EmptyState';
import { ErrorState } from '../components/ui/ErrorState';
import { SkeletonList } from '../components/ui/Skeleton';

export function AssistantPage() {
  const { t } = useTranslation();
  const conversations = useAIConversations();
  const ask = useAskAI();
  const [question, setQuestion] = useState('');
  const [conversationId, setConversationId] = useState<string | undefined>();
  const [answer, setAnswer] = useState('');
  const [citations, setCitations] = useState<AICitation[]>([]);

  function submit(event: FormEvent) {
    event.preventDefault();
    const trimmed = question.trim();
    if (!trimmed) return;
    ask.mutate(
      { question: trimmed, conversation_id: conversationId },
      {
        onSuccess: (data) => {
          setConversationId(data.conversation_id);
          setAnswer(data.answer);
          setCitations(data.citations);
          setQuestion('');
        },
      },
    );
  }

  if (conversations.isLoading) return <SkeletonList rows={4} label={t('assistant.loading')} />;
  if (conversations.isError)
    return (
      <ErrorState
        title={t('assistant.errorTitle')}
        description={conversations.error.message}
        retryLabel={t('common.retry')}
        onRetry={() => conversations.refetch()}
      />
    );

  return (
    <div className="grid gap-6 lg:grid-cols-[18rem,1fr]">
      <Card title={t('assistant.conversations')}>
        {conversations.data?.length ? (
          <ul className="space-y-2">
            {conversations.data.map((conversation) => (
              <li key={conversation.id}>
                <button
                  className="w-full rounded-lg px-3 py-2 text-left text-sm hover:bg-gray-100 focus:outline-none focus:ring-2 focus:ring-primary dark:hover:bg-gray-900"
                  onClick={() => setConversationId(conversation.id)}
                >
                  {conversation.title}
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <EmptyState title={t('assistant.empty')} description={t('assistant.emptyHint')} />
        )}
      </Card>
      <div className="space-y-6">
        <div>
          <h1 className="text-2xl font-bold text-gray-900 dark:text-white">
            {t('assistant.title')}
          </h1>
          <p className="text-sm text-gray-600 dark:text-gray-300">{t('assistant.subtitle')}</p>
        </div>
        <Card title={t('assistant.askTitle')}>
          <form onSubmit={submit} className="space-y-3">
            <label className="block text-sm font-medium" htmlFor="assistant-question">
              {t('assistant.questionLabel')}
            </label>
            <textarea
              id="assistant-question"
              className="min-h-32 w-full rounded-lg border border-gray-300 bg-white p-3 text-sm dark:border-gray-700 dark:bg-gray-950"
              value={question}
              onChange={(event) => setQuestion(event.target.value)}
              placeholder={t('assistant.placeholder')}
            />
            <Button type="submit" loading={ask.isPending}>
              {t('assistant.ask')}
            </Button>
          </form>
          {ask.isError ? <p className="mt-3 text-sm text-red-600">{ask.error.message}</p> : null}
        </Card>
        {answer ? (
          <Card title={t('assistant.answer')}>
            <p className="whitespace-pre-wrap text-sm leading-6">{answer}</p>
            <h2 className="mt-4 text-sm font-semibold">{t('assistant.citations')}</h2>
            <ul className="mt-2 space-y-2">
              {citations.map((citation) => (
                <li key={`${citation.entity_type}-${citation.entity_id}`} className="text-sm">
                  <a className="text-primary underline" href={citation.url}>
                    {citation.title}
                  </a>{' '}
                  <span className="text-gray-500">({citation.entity_type})</span>
                </li>
              ))}
            </ul>
          </Card>
        ) : null}
      </div>
    </div>
  );
}
