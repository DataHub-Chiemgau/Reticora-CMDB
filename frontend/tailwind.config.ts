import type { Config } from 'tailwindcss';

/**
 * Reticora design tokens (see frontend/DESIGN.md).
 *
 * Two layers:
 *  - Semantic tokens (canvas, panel, line, fg, accent …) resolve to CSS
 *    variables that src/index.css switches per theme. New components use them,
 *    so they need no `dark:` variants.
 *  - The `gray` and `blue` scales are redefined here, so every page written
 *    with the Tailwind palette (gray-*, blue-*) follows the same identity:
 *    a cool graphite neutral and one petrol accent.
 */
const channel = (name: string) => `hsl(var(--${name}) / <alpha-value>)`;

const config: Config = {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        // Semantic tokens.
        canvas: channel('canvas'),
        panel: channel('panel'),
        raised: channel('raised'),
        control: channel('control'),
        line: {
          DEFAULT: channel('line'),
          strong: channel('line-strong'),
        },
        fg: {
          DEFAULT: channel('fg'),
          muted: channel('fg-muted'),
          subtle: channel('fg-subtle'),
        },
        accent: {
          DEFAULT: channel('accent'),
          strong: channel('accent-strong'),
          soft: channel('accent-soft'),
          fg: channel('accent-fg'),
        },
        // Legacy aliases kept for the pages written before the token layer.
        primary: channel('color-primary'),
        surface: channel('color-surface'),
        // Cool graphite neutral. 500 and 600 are the shared secondary text
        // colours (4.5:1 on white and on the canvas), 900/950 are the dark
        // theme's panel and canvas.
        gray: {
          50: '#F7F9FA',
          100: '#EFF2F4',
          200: '#E1E6E9',
          300: '#CBD3D8',
          400: '#98A4AC',
          500: '#5D6D76',
          600: '#4E5C65',
          700: '#3B474F',
          800: '#262F36',
          900: '#151B20',
          950: '#0D1215',
        },
        // The accent family. Information, links and selection are petrol.
        blue: {
          50: '#E8F5F6',
          100: '#D0EBED',
          200: '#A8DADF',
          300: '#7CC6CD',
          400: '#4CC4CE',
          500: '#1E9AA6',
          600: '#087680',
          700: '#065F68',
          800: '#064A52',
          900: '#073A41',
          950: '#04262B',
        },
      },
      fontFamily: {
        sans: [
          '"Geist Variable"',
          'ui-sans-serif',
          'system-ui',
          '-apple-system',
          '"Segoe UI"',
          'Roboto',
          'sans-serif',
        ],
        mono: [
          '"Geist Mono Variable"',
          'ui-monospace',
          'SFMono-Regular',
          'Menlo',
          'Consolas',
          'monospace',
        ],
      },
      // Type scale: 12 / 14 / 15 / 17 / 19 / 24 / 30. Page titles are 2xl.
      fontSize: {
        xs: ['0.75rem', { lineHeight: '1rem' }],
        sm: ['0.875rem', { lineHeight: '1.25rem' }],
        base: ['0.9375rem', { lineHeight: '1.5rem' }],
        lg: ['1.0625rem', { lineHeight: '1.5rem', letterSpacing: '-0.01em' }],
        xl: ['1.1875rem', { lineHeight: '1.625rem', letterSpacing: '-0.012em' }],
        '2xl': ['1.5rem', { lineHeight: '2rem', letterSpacing: '-0.02em' }],
        '3xl': ['1.875rem', { lineHeight: '2.25rem', letterSpacing: '-0.025em' }],
      },
      // Bold is semibold in this system: two weights carry the hierarchy
      // (500 for interactive labels, 600 for headings and figures).
      fontWeight: {
        bold: '600',
      },
      // Radii: 6 chips, 8 controls, 10 menus, 12 panels and dialogs.
      borderRadius: {
        sm: '4px',
        DEFAULT: '6px',
        md: '6px',
        lg: '8px',
        xl: '10px',
        '2xl': '12px',
        '3xl': '16px',
      },
      // Shadows are for floating layers. Panels are separated by borders.
      boxShadow: {
        sm: '0 1px 2px 0 rgb(13 18 21 / 0.05)',
        DEFAULT: '0 1px 3px 0 rgb(13 18 21 / 0.07), 0 1px 2px -1px rgb(13 18 21 / 0.07)',
        md: '0 4px 12px -2px rgb(13 18 21 / 0.08), 0 2px 4px -2px rgb(13 18 21 / 0.06)',
        lg: '0 12px 28px -8px rgb(13 18 21 / 0.16), 0 4px 8px -4px rgb(13 18 21 / 0.08)',
        xl: '0 20px 40px -12px rgb(13 18 21 / 0.22), 0 6px 12px -6px rgb(13 18 21 / 0.1)',
        '2xl': '0 28px 64px -16px rgb(13 18 21 / 0.35), 0 8px 16px -8px rgb(13 18 21 / 0.14)',
      },
      transitionDuration: {
        DEFAULT: '120ms',
      },
      keyframes: {
        'fade-in': { from: { opacity: '0' }, to: { opacity: '1' } },
        'pop-in': {
          from: { opacity: '0', transform: 'translate(-50%, -48%) scale(0.98)' },
          to: { opacity: '1', transform: 'translate(-50%, -50%) scale(1)' },
        },
        'pop-in-top': {
          from: { opacity: '0', transform: 'translate(-50%, -6px) scale(0.985)' },
          to: { opacity: '1', transform: 'translate(-50%, 0) scale(1)' },
        },
        'menu-in': {
          from: { opacity: '0', transform: 'translateY(-4px) scale(0.98)' },
          to: { opacity: '1', transform: 'translateY(0) scale(1)' },
        },
        'drawer-in': {
          from: { transform: 'translateX(-100%)' },
          to: { transform: 'translateX(0)' },
        },
        'toast-in': {
          from: { opacity: '0', transform: 'translateY(8px)' },
          to: { opacity: '1', transform: 'translateY(0)' },
        },
      },
      animation: {
        'fade-in': 'fade-in 140ms ease-out',
        'pop-in': 'pop-in 160ms cubic-bezier(0.16, 1, 0.3, 1)',
        'pop-in-top': 'pop-in-top 160ms cubic-bezier(0.16, 1, 0.3, 1)',
        'menu-in': 'menu-in 130ms cubic-bezier(0.16, 1, 0.3, 1)',
        'drawer-in': 'drawer-in 200ms cubic-bezier(0.16, 1, 0.3, 1)',
        'toast-in': 'toast-in 180ms cubic-bezier(0.16, 1, 0.3, 1)',
      },
    },
  },
  plugins: [],
};

export default config;
