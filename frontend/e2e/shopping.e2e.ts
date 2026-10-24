import { test, expect, type Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
const config = JSON.parse(readFileSync(process.env.CARTSIDE_E2E_CONFIG!, 'utf8')) as {
  origin: string; deployment?: boolean; users: { username: string; password: string }[]; compose: string[]
}
async function login(page: Page, user = 0) {
  try {
    await page.goto(config.origin)
    await page.locator('#username').fill(config.users[user].username)
    await page.locator('#password').fill(config.users[user].password)
    await page.locator('#kc-login').click()
    await expect(page.getByRole('button', { name: 'Refresh cart' })).toBeEnabled()
  } catch {
    await page.goto('about:blank').catch(() => {})
    throw new Error('Sign-in failed (credential and navigation details suppressed)')
  }
}
async function api(page: Page, path: string, method = 'GET', body?: unknown) {
  return page.evaluate(async ({ path, method, body }) => {
    const r = await fetch('/api/'+path, { method, headers: { 'Content-Type': 'application/json', 'X-CSRF-Protection': '1' }, body: body === undefined ? undefined : JSON.stringify(body) })
    return { status: r.status, body: await r.json().catch(() => null) }
  }, { path, method, body })
}
async function add(page: Page) {
  const button = page.getByRole('button', { name: /^Add .+ to cart$/ }).first()
  await expect(button).toBeEnabled()
  await button.click()
  await expect(page.getByText('1 total units · 1 distinct items')).toBeVisible()
}
async function review(page: Page) {
  await page.getByRole('button', { name: 'Review simulated order', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Place simulated order' })).toBeEnabled()
}
function sql(statement: string) {
  execFileSync(config.compose[0], [...config.compose.slice(1), 'exec', '-T', 'postgres', 'psql', '-U', 'e2e', '-d', 'e2e', '-v', 'ON_ERROR_STOP=1', '-qAt'], { input: statement, stdio: ['pipe', 'pipe', 'pipe'] })
}
async function stored(page: Page) {
  return page.evaluate(() => Object.entries(sessionStorage).filter(([key]) => key.startsWith('cartside.checkout.v1:')))
}

// Playwright can generate error-context.md even with tracing disabled. Remove
// sensitive call details and blank the page before its failure-artifact teardown.
test.afterEach(async ({ context }, info) => {
  for (const error of info.errors) {
    const category = error.message?.includes('strict mode violation') ? 'ambiguous locator' : error.message?.includes('Timeout') ? 'timeout' : 'assertion'
    const location = error.stack?.match(/shopping\.e2e\.ts:\d+:\d+/)?.[0] ?? 'unknown location'
    for (const key of Object.keys(error)) delete (error as Record<string, unknown>)[key]
    error.message = `Browser ${category} failed at ${location}; sensitive details suppressed.`
  }
  await Promise.all(context.pages().map(page => page.goto('about:blank').catch(() => {})))
})

test('sign-in, search, pagination, cart persistence, isolation, keyboard/mobile checkout and logout', async ({ page, browser }) => {
  let hmrConnected = false
  page.on('websocket', socket => {
    if (socket.url().startsWith(config.origin.replace('http:', 'ws:') + '/')) {
      socket.on('framereceived', event => {
        if (event.payload.toString().includes('"type":"connected"')) hmrConnected = true
      })
    }
  })
  await page.goto(config.origin+'/api/cart')
  expect((await page.request.get(config.origin+'/api/cart')).status()).toBe(401)
  await login(page)
  if (!config.deployment) await expect.poll(() => hmrConnected).toBe(true)
  else {
    const sessionCookies = (await page.context().cookies()).filter(cookie => cookie.name.startsWith('session'))
    expect(sessionCookies.length).toBeGreaterThan(0)
    expect(sessionCookies.every(cookie => cookie.secure && cookie.httpOnly && cookie.sameSite === 'Lax')).toBe(true)
    const assets = await page.evaluate(async () => {
      const urls = [...document.querySelectorAll<HTMLScriptElement>('script[src]')].map(node => node.src)
        .concat([...document.querySelectorAll<HTMLLinkElement>('link[rel="stylesheet"]')].map(node => node.href))
      return Promise.all(urls.map(async url => {
        const response = await fetch(url)
        return { path: new URL(url).pathname, status: response.status, type: response.headers.get('content-type') }
      }))
    })
    expect(assets.some(asset => asset.path.startsWith('/assets/') && asset.type?.includes('javascript') && asset.status === 200)).toBe(true)
    expect(assets.some(asset => asset.path.startsWith('/assets/') && asset.type?.includes('text/css') && asset.status === 200)).toBe(true)
    expect((await page.request.post(config.origin+'/api/cart/items', { data: {} })).status()).toBe(403)
    expect((await page.request.post(config.origin+'/api/cart/items', {
      headers: { 'X-CSRF-Protection': '1', Origin: 'https://other.invalid' }, data: {},
    })).status()).toBe(403)
    for (const path of ['/internal/ai/chat/completions', '/metrics', '/admin/', '/apisix/']) {
      expect((await page.request.get(config.origin+path)).status()).toBe(404)
    }
  }
  // The same cookie jar has a verified storefront session, but no AI service key.
  expect((await page.request.get(config.origin+'/auth/session')).status()).toBe(200)
  expect((await page.request.post(config.origin+'/internal/ai/chat/completions', {
    data: { model: 'cartside-mock-v1', messages: [{ role: 'user', content: 'breakfast for two' }] },
  })).status()).toBe(config.deployment ? 404 : 401)
  const snapshot = () => execFileSync(config.compose[0], [...config.compose.slice(1), 'exec', '-T', 'postgres', 'psql', '-U', 'e2e', '-d', 'e2e', '-qAt'], { input: 'SELECT json_build_array((SELECT json_agg(c) FROM carts c),(SELECT json_agg(i) FROM cart_items i),(SELECT json_agg(o) FROM orders o),(SELECT json_agg(i) FROM order_items i));', stdio: ['pipe', 'pipe', 'pipe'] }).toString()
  const beforeData = snapshot()
  const beforeAssistant = await api(page, 'cart')
  const assistant = page.getByRole('region', { name: 'Demo shopping assistant' })
  await assistant.getByRole('button', { name: 'breakfast for two', exact: true }).click()
  await assistant.getByRole('button', { name: 'Send message' }).click()
  await expect(assistant.getByText('simulated: cartside-mock-v1', { exact: true })).toBeVisible()
  const suggestions = assistant.locator('.assistant-suggestions > li')
  await expect(suggestions.first().getByRole('button', { name: /^Accept / })).toBeEnabled()
  expect(await suggestions.count()).toBeGreaterThan(1)
  expect((await api(page, 'cart')).body).toEqual(beforeAssistant.body)
  expect(snapshot()).toBe(beforeData)
  const selectedName = await suggestions.first().getByRole('heading').innerText()
  const suggestedQuantity = Number((await suggestions.first().innerText()).match(/Quantity: (\d+)/)![1])
  await suggestions.first().getByRole('button', { name: /^Accept / }).evaluate(button => { (button as HTMLButtonElement).click(); (button as HTMLButtonElement).click() })
  await expect(suggestions.first().getByText('Accepted. Added to your cart.', { exact: true })).toBeVisible()
  await expect(suggestions.first().getByRole('button', { name: /^Accept / })).toBeDisabled()
  const acceptedCart = (await api(page, 'cart')).body
  expect(acceptedCart.items.length).toBe(1)
  expect(acceptedCart.items[0].product.name).toBe(selectedName)
  expect(acceptedCart.items[0].quantity).toBe(suggestedQuantity)
  await suggestions.nth(1).getByRole('button', { name: /^Decline / }).click()
  await expect(suggestions.nth(1).getByText('Declined. Nothing was added.')).toBeVisible()
  expect((await api(page, 'cart')).body).toEqual(acceptedCart)
  await page.getByRole('button', { name: `Remove ${selectedName}`, exact: true }).click()
  await expect(page.getByText('Your cart is empty.', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: 'Next', exact: true }).click()
  await expect(page.getByText('Page 2', { exact: true })).toBeVisible()
  await page.getByLabel('Search groceries').fill('broccxli')
  await page.getByLabel('Search groceries').press('Enter')
  await expect(page.getByRole('heading', { name: 'Broccoli Crowns' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Broccoli Florets' })).toBeVisible()
  await page.getByRole('button', { name: 'Clear', exact: true }).click()
  await add(page)
  const before = await api(page, 'cart')
  const item = before.body.items[0]
  const quantity = page.getByLabel(`Quantity for ${item.product.name}`, { exact: true })
  await quantity.fill('3')
  await page.getByRole('button', { name: `Update quantity for ${item.product.name}`, exact: true }).click()
  await expect(page.getByText('3 total units · 1 distinct items')).toBeVisible()
  const changed = await api(page, 'cart')
  expect(changed.body.subtotal_cents === 3 * item.product.price_cents).toBe(true)
  await page.reload()
  await expect(page.getByText('3 total units · 1 distinct items')).toBeVisible()
  const other = await browser.newContext({ ignoreHTTPSErrors: !!config.deployment })
  try {
    const bob = await other.newPage(); await login(bob, 1)
    expect((await api(bob, 'cart')).body.items.length).toBe(0)
  } finally { await other.close() }
  await page.getByRole('button', { name: `Remove ${item.product.name}`, exact: true }).click()
  await expect(page.getByText('Your cart is empty.', { exact: false })).toBeVisible()
  await add(page)
  await page.setViewportSize({ width: 375, height: 812 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  // Mix an assistant increment with an existing manually edited cart.
  await assistant.getByRole('button', { name: 'breakfast for two', exact: true }).click()
  await assistant.getByRole('button', { name: 'Send message' }).click()
  await expect(suggestions.first().getByRole('button', { name: /^Accept / })).toBeEnabled()
  const manualCart = (await api(page, 'cart')).body
  await suggestions.first().getByRole('button', { name: /^Accept / }).click()
  await expect(suggestions.first().getByText('Accepted. Added to your cart.', { exact: true })).toBeVisible()
  const reviewed = (await api(page, 'cart')).body
  const existingQuantity = manualCart.items.find((i: { product: { name: string } }) => i.product.name === selectedName)?.quantity ?? 0
  expect(reviewed.items.find((i: { product: { name: string } }) => i.product.name === selectedName).quantity).toBe(existingQuantity + suggestedQuantity)
  let confirmed: unknown
  page.on('response', async response => {
    if (response.url() === config.origin + '/api/checkout' && response.status() === 200) confirmed = await response.json()
  })
  await review(page)
  const section = page.getByRole('region', { name: 'Simulated checkout', exact: true })
  await expect(section.getByText(new RegExp(`Quantity: ${reviewed.items[0].quantity}`)).first()).toBeVisible()
  expect((await api(page, 'cart')).body).toEqual(reviewed)
  await expect(page.getByRole('heading', { name: 'Simulated checkout', exact: true })).toBeFocused()
  await page.keyboard.press('Tab')
  await expect(page.getByRole('button', { name: 'Back to cart' })).toBeFocused()
  await page.keyboard.press('Tab')
  await expect(page.getByRole('button', { name: 'Place simulated order' })).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page.getByRole('heading', { name: 'Simulated order confirmed' })).toBeVisible()
  await expect.poll(() => confirmed).toBeTruthy()
  expect(confirmed).toMatchObject({ total_cents: reviewed.subtotal_cents, items: reviewed.items.map((i: { product: { id: string; name: string; package_label: string; price_cents: number }; quantity: number; line_total_cents: number }) => ({ product_id: i.product.id, name: i.product.name, package_label: i.product.package_label, quantity: i.quantity, unit_price_cents: i.product.price_cents, line_total_cents: i.line_total_cents })) })
  expect((await api(page, 'cart')).body.items.length).toBe(0)
  await page.getByRole('link', { name: 'Sign out', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'You are signed out' })).toBeVisible()
  expect((await page.request.get(config.origin+'/api/cart')).status()).toBe(401)
})

test('committed response loss, reload, session renewal, same-body replay, double click and failed cart refresh', async ({ page }) => {
  await login(page); await add(page); await review(page)
  let originalKey = ''; let originalBody = ''; let originalConfirmation = ''
  let requests = 0
  await page.route('**/api/checkout', async route => {
    requests++
    const request = route.request()
    if (requests === 1) {
      originalKey = request.headers()['idempotency-key']; originalBody = request.postData()!
      const response = await route.fetch()
      expect(response.status()).toBe(200)
      originalConfirmation = (await response.json()).confirmation
      await route.abort('failed') // The real transaction committed before transport loss.
    } else {
      expect(request.headers()['idempotency-key'] === originalKey && request.postData() === originalBody).toBe(true)
      await route.continue()
    }
  })
  await page.getByRole('button', { name: 'Place simulated order' }).evaluate(button => { (button as HTMLButtonElement).click(); (button as HTMLButtonElement).click() })
  await expect(page.getByText(/Checkout outcome is unresolved/)).toBeVisible()
  expect(requests).toBe(1)
  const saved = await stored(page)
  await page.reload()
  await expect(page.getByRole('button', { name: 'Retry same checkout' })).toBeEnabled()
  await expect(page.getByRole('button', { name: /^Add .+ to cart$/ }).first()).toBeDisabled()
  // Genuine logout/login renews the session while preserving the tab's storage.
  await page.getByRole('link', { name: 'Sign out', exact: true }).click()
  await login(page, 1)
  expect(await page.getByRole('button', { name: 'Retry same checkout' }).count()).toBe(0)
  expect(JSON.stringify(await stored(page)) === JSON.stringify(saved)).toBe(true)
  await page.getByRole('link', { name: 'Sign out', exact: true }).click()
  await login(page)
  await expect(page.getByRole('button', { name: 'Retry same checkout' })).toBeEnabled()
  await page.route('**/api/cart', route => route.request().method() === 'GET' ? route.fulfill({ status: 503, body: 'unavailable' }) : route.continue())
  await page.getByRole('button', { name: 'Retry same checkout' }).click()
  await expect(page.getByText(originalConfirmation, { exact: true })).toBeVisible()
  await expect(page.getByText(/Order confirmed. Cart refresh failed/)).toBeVisible()
  expect(requests).toBe(2)
  expect((await stored(page)).length).toBe(0)
  await page.unroute('**/api/cart')
  await page.getByRole('button', { name: 'Refresh cart' }).click()
  await expect(page.getByText('Your cart is empty.', { exact: false })).toBeVisible()
  await expect(page.getByText(originalConfirmation, { exact: true })).toBeVisible()
  expect(/^[0-9a-f-]{36}$/.test(originalKey)).toBe(true)
  sql(`DO $$ BEGIN IF (SELECT count(*) FROM orders WHERE idempotency_key='${originalKey}') <> 1 THEN RAISE EXCEPTION 'unexpected attempt order count'; END IF; END $$;`)
})

for (const failure of ['price', 'unavailable', 'stale'] as const) {
  test(`${failure} rejection after uncertainty requires renewed review`, async ({ page }) => {
    await login(page); await add(page); await review(page)
    const cart = (await api(page, 'cart')).body
    const product = cart.items[0].product
    expect(/^[0-9a-f-]{36}$/.test(product.id)).toBe(true)
    // First request never arrives: the client must still preserve it as uncertain.
    await page.route('**/api/checkout', route => route.abort('failed'))
    await page.getByRole('button', { name: 'Place simulated order' }).click()
    await expect(page.getByText(/Checkout outcome is unresolved/)).toBeVisible()
    try {
      if (failure === 'price') sql(`UPDATE products SET price_cents=price_cents+1 WHERE id='${product.id}';`)
      if (failure === 'unavailable') sql(`UPDATE products SET available=false WHERE id='${product.id}';`)
      if (failure === 'stale') expect((await api(page, 'cart/items/'+product.id, 'PUT', { quantity: 2 })).status).toBe(204)
      await page.unroute('**/api/checkout')
      await page.getByRole('button', { name: 'Retry same checkout' }).click()
      await expect(page.getByText(/Checkout rejected/)).toBeVisible()
      expect((await stored(page)).length).toBe(0)
      expect(await page.getByRole('button', { name: 'Place simulated order' }).count()).toBe(0)
    } finally {
      sql(`UPDATE products SET price_cents=${product.price_cents}, available=true WHERE id='${product.id}';`)
      await api(page, 'cart/items/'+product.id, 'DELETE')
    }
  })
}


test('deployment identity routes and canonical issuer', async ({ page }) => {
  test.skip(!config.deployment, 'Production-style HTTPS runner only')
  const identity = config.origin.replace('://cartside.', '://auth.cartside.')
  for (const path of ['/admin/', '/realms/master/', '/health/ready', '/metrics']) {
    expect((await page.request.get(identity+path)).status()).toBe(404)
  }
  const discovery = await page.request.get(identity+'/realms/cartside/.well-known/openid-configuration')
  expect(discovery.status()).toBe(200)
  const metadata = await discovery.json()
  expect(metadata.issuer).toBe(identity+'/realms/cartside')
  expect(metadata.authorization_endpoint.startsWith(identity+'/')).toBe(true)
  await page.goto(config.origin)
  expect(page.url().startsWith(identity+'/')).toBe(true)
})
