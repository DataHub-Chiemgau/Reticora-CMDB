import { defineConfig } from 'vitest/config';
import base from './vitest.config';

// Phase-1 acceptance audit (audit/phase1-final/PHASE1_FINDINGS.md): unit tests
// that encode a requirement the product currently violates. They are named
// *.auditdefect.ts(x), so the default `npm test` does not pick them up, and
// they fail until the finding is fixed. Run:
//   npx vitest run -c vitest.auditdefect.config.ts
export default defineConfig({
  ...base,
  test: {
    ...base.test,
    include: ['src/**/*.auditdefect.{ts,tsx}'],
  },
});
