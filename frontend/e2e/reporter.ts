import type { Reporter, TestCase, TestResult } from '@playwright/test/reporter'
// Never print Playwright call logs: authentication actions can contain credentials.
export default class SafeReporter implements Reporter {
  onTestEnd(test: TestCase, result: TestResult) {
    process.stdout.write(`CARTSIDE-E2E ${test.title}: ${result.status}\n`)
    for (const error of result.errors) {
      const location = error.message?.match(/shopping\.e2e\.ts:\d+:\d+/)?.[0]
      if (location) {
        const category = error.message?.includes('ambiguous locator') ? 'ambiguous locator' : error.message?.includes('timeout') ? 'timeout' : 'assertion'
        process.stdout.write(`CARTSIDE-E2E ${category} near ${location}\n`)
      }
    }
  }
  onError() { process.stdout.write('CARTSIDE-E2E configuration or worker: failed\n') }
}
