import { Monitor, Moon, Sun } from 'lucide-react';
import type { ThemeMode } from '../../stores/theme';
import { IconButton } from '../ui/Button';

/** ThemeIcon shows the current theme mode: sun (light), moon (dark), monitor (system). */
export function ThemeIcon({ mode }: { mode: ThemeMode }) {
  if (mode === 'light') return <Sun />;
  if (mode === 'dark') return <Moon />;
  return <Monitor />;
}

/**
 * ThemeToggle cycles the theme (stores/theme.ts nextThemeMode). Its name
 * states the current mode and the one the next click selects.
 */
export function ThemeToggle({
  mode,
  label,
  onToggle,
}: {
  mode: ThemeMode;
  label: string;
  onToggle: () => void;
}) {
  return <IconButton label={label} icon={<ThemeIcon mode={mode} />} onClick={onToggle} />;
}
