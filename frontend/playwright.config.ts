import { dirname, join } from 'node:path'
import { defineConfig } from '@playwright/test'
if (!process.env.CARTSIDE_E2E_CONFIG) throw new Error('Invoke python3 scripts/e2e.py from the repository root')
export default defineConfig({
  outputDir: join(dirname(process.env.CARTSIDE_E2E_CONFIG!), 'browser-output'), preserveOutput: 'never',
  testDir: './e2e', testMatch: '**/*.e2e.ts', workers: 1, retries: 0, timeout: 60000,
  reporter: [['./e2e/reporter.ts']],
  use: { ignoreHTTPSErrors: process.env.CARTSIDE_E2E_HTTPS === '1', browserName: 'chromium', trace: 'off', screenshot: 'off', video: 'off' },
})
