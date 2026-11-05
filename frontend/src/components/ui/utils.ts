import { twMerge } from 'tailwind-merge';

/**
 * cn joins class names and resolves conflicts: of two classes that set the
 * same property (`h-9` and `h-8`, `w-full` and `w-auto`) the later one wins,
 * so a component's own className always overrides its defaults, whatever
 * order Tailwind emits the rules in.
 */
export function cn(...classes: Array<string | false | null | undefined>) {
  return twMerge(classes.filter(Boolean).join(' '));
}
