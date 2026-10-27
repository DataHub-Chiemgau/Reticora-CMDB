/**
 * E-99 (WP-523): the language switch in the header changes the UI language,
 * stores the choice for this browser and is operable by keyboard.
 */
import { afterEach, describe, expect, it } from 'vitest';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { useTranslation } from 'react-i18next';
import i18n, { LANGUAGE_STORAGE_KEY } from '../i18n';
import { renderWithProviders } from '../test/utils';
import { LanguageSwitch } from './LanguageSwitch';

/** A text of the page that follows the language. */
function Probe() {
  const { t } = useTranslation();
  return <p>{t('auth.logout')}</p>;
}

afterEach(async () => {
  window.localStorage.clear();
  await act(async () => {
    await i18n.changeLanguage('de-DE');
  });
});

describe('LanguageSwitch', () => {
  it('is a named select with the German and English option', () => {
    renderWithProviders(<LanguageSwitch />);
    const select = screen.getByRole('combobox', { name: 'Sprache' });
    expect(select).toHaveValue('de-DE');
    expect(
      screen
        .getAllByRole('option')
        .map((option) => [option.getAttribute('value'), option.textContent]),
    ).toEqual([
      ['de-DE', 'Deutsch'],
      ['en-US', 'English'],
    ]);
  });

  it('switches the UI to English and stores the choice', async () => {
    renderWithProviders(
      <>
        <LanguageSwitch />
        <Probe />
      </>,
    );
    const before = screen.getByText(i18n.t('auth.logout')).textContent;

    fireEvent.change(screen.getByRole('combobox', { name: 'Sprache' }), {
      target: { value: 'en-US' },
    });

    const select = await screen.findByRole('combobox', { name: 'Language' });
    expect(select).toHaveValue('en-US');
    expect(i18n.language).toBe('en-US');
    expect(window.localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe('en-US');
    expect(screen.getByText(i18n.getFixedT('en-US')('auth.logout'))).toBeInTheDocument();
    expect(screen.queryByText(before!)).not.toBeInTheDocument();
  });

  it('switches by keyboard', async () => {
    renderWithProviders(<LanguageSwitch />);
    const select = screen.getByRole('combobox', { name: 'Sprache' });
    select.focus();
    expect(select).toHaveFocus();

    fireEvent.keyDown(select, { key: 'ArrowDown' });
    await waitFor(() => expect(i18n.language).toBe('en-US'));
    expect(window.localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe('en-US');
    expect(screen.getByRole('combobox', { name: 'Language' })).toHaveFocus();

    fireEvent.keyDown(screen.getByRole('combobox', { name: 'Language' }), { key: 'ArrowUp' });
    await waitFor(() => expect(i18n.language).toBe('de-DE'));
    expect(window.localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe('de-DE');
  });

  it('follows a language change made elsewhere', async () => {
    renderWithProviders(<LanguageSwitch />);
    await act(async () => {
      await i18n.changeLanguage('en-US');
    });
    expect(screen.getByRole('combobox', { name: 'Language' })).toHaveValue('en-US');
  });
});
