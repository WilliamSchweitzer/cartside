// Called by verify-observability.py --browser against its disposable stack.
import { createRequire } from 'node:module';
const require = createRequire(new URL('../frontend/package.json', import.meta.url));
const { chromium } = require('@playwright/test');
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage();
try {
  await page.goto(process.env.GRAFANA_TEST_URL + '/login');
  await page.locator('input[name="user"]').fill('local-admin');
  await page.locator('input[name="password"]').fill(process.env.GRAFANA_TEST_PASSWORD);
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await page.waitForURL(url => !url.pathname.startsWith('/login'), { timeout: 30000 });
  await page.goto(process.env.GRAFANA_TEST_URL + '/d/cartside-overview');
  for (const title of ['Target scrape health (1 = UP)', 'Assistant failure outcomes', 'Gateway p95 request latency']) {
    await page.getByText(title, { exact: true }).scrollIntoViewIfNeeded({ timeout: 30000 });
  }
  console.log('PASS: Chromium Grafana login and overview panels rendered');
} catch (error) {
  await page.screenshot({ path: process.env.GRAFANA_TEST_SCREENSHOT || '/tmp/cartside-grafana-check.png', fullPage: true });
  console.error('Browser URL at failure:', page.url());
  throw error;
} finally {
  await browser.close();
}
