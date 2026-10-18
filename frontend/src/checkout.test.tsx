import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Checkout, { storageKey } from './Checkout'
import CartView from './Cart'
import { useCart } from './useCart'
import { reviewBody, validAttempt, validOrder } from './checkoutApi'
import type { Cart } from './cartApi'
import type { Identity } from './App'
const product = { id: 'bb5ce88f-908c-5a99-8ae7-90b60b00ae1b', name: 'Apples', package_label: 'bag', price_cents: 125, currency: 'USD' as const, image_reference: '', category: 'produce', available: true }
const cart: Cart = { version: 7, items: [{ product, quantity: 2, line_total_cents: 250 }], subtotal_cents: 250, currency: 'USD' }
const order = { id: 'd550df71-4280-480b-a733-1ee7e24c1ab5', confirmation: 'SIM-test', kind: 'simulated', created_at: '2026-10-15T12:00:00Z', currency: 'USD', total_cents: 250, items: [{ product_id: product.id, name: 'Apples snapshot', package_label: 'bag snapshot', quantity: 2, unit_price_cents: 125, line_total_cents: 250 }] }
const identity: Identity = { state: 'ready', issuer: 'issuer', subject: 'alice' }
const key = storageKey('issuer', 'alice')
const json = (v: unknown, status = 200, verified = false) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json', ...(verified ? { 'X-Cartside-Checkout-Rejection': 'no-matching-order' } : {}) } })
function Harness({ user = identity }: { user?: Identity }) {
  const state = useCart(true)
  return <><button onClick={() => void state.mutate('POST', product.id, product.name, 1)}>Force mutation</button><button disabled={state.busy || state.blocked} onClick={() => void state.mutate('POST', product.id, product.name, 1)}>Add</button><CartView state={state} /><Checkout state={state} identity={user} /></>
}
function setup() {
  const calls: RequestInit[] = []
  let checkout: (init: RequestInit) => Promise<Response> = async () => json(order)
  let reads = 0
  let read: () => Response = () => json(cart)
  const fetcher = vi.fn(async (url: string, init: RequestInit) => {
    if (url === '/api/checkout') { calls.push(init); return checkout(init) }
    if (init.method === 'GET') { reads++; return read() }
    return new Response(null, { status: 204 })
  })
  vi.stubGlobal('fetch', fetcher)
  return { calls, fetcher, checkout: (fn: typeof checkout) => { checkout = fn }, read: (fn: typeof read) => { read = fn }, reads: () => reads }
}
async function review() {
  const button = screen.getByRole('button', { name: 'Review simulated order' })
  await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false))
  fireEvent.click(button)
  await screen.findByRole('button', { name: 'Place simulated order' })
}
async function place() { await review(); fireEvent.click(screen.getByRole('button', { name: 'Place simulated order' })) }
afterEach(() => { cleanup(); sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers() })
describe('simulated checkout', () => {
  it('constructs complete review input and validates order totals and snapshots', () => {
    const attempt = { key: crypto.randomUUID(), body: reviewBody(cart) }
    expect(JSON.parse(attempt.body)).toEqual({ expected_cart_version: 7, items: [{ product_id: product.id, quantity: 2, unit_price_cents: 125 }] })
    expect(validAttempt(attempt)).toBe(true)
    expect(validAttempt({ ...attempt, body: '{}' })).toBe(false)
    expect(validOrder(order, attempt)).toBe(true)
    expect(validOrder({ ...order, total_cents: 1 }, attempt)).toBe(false)
    expect(validOrder({ ...order, items: [order.items[0], order.items[0]] }, attempt)).toBe(false)
  })
  it('refreshes before review, requires explicit confirmation, guards duplicate clicks and refreshes after success', async () => {
    const s = setup(); let finish!: (r: Response) => void
    s.checkout(() => new Promise(resolve => { finish = resolve }))
    render(<Harness />); await review()
    expect(s.reads()).toBe(2); expect(s.calls).toHaveLength(0)
    const section = screen.getByRole('region', { name: 'Simulated checkout' })
    expect(within(section).getByText(/Apples · bag · Quantity: 2 · Unit price: \$1.25 · Line total: \$2.50/)).toBeTruthy()
    expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'Simulated checkout' }))
    const button = screen.getByRole('button', { name: 'Place simulated order' })
    act(() => { fireEvent.click(button); fireEvent.click(button) })
    expect(s.calls).toHaveLength(1)
    const requests = s.fetcher.mock.calls.length
    fireEvent.click(screen.getByText('Force mutation'))
    expect(s.fetcher.mock.calls).toHaveLength(requests)
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Remove Apples' }) as HTMLButtonElement).disabled).toBe(true)
    expect(s.calls[0].headers).toEqual({ 'X-CSRF-Protection': '1', 'Content-Type': 'application/json', 'Idempotency-Key': JSON.parse(sessionStorage.getItem(key)!).key })
    expect(s.calls[0].credentials).toBe('same-origin')
    await act(async () => finish(json(order)))
    await screen.findByText('SIM-test'); expect(s.reads()).toBe(3)
    expect(sessionStorage.getItem(key)).toBeNull()
    expect(screen.getByText(/Apples snapshot/)).toBeTruthy()
  })
  it('keeps exact key and body after network loss and reload, waits for identity and isolates users', async () => {
    const s = setup(); s.checkout(async () => { throw new TypeError('lost') })
    const view = render(<Harness />); await place()
    await screen.findByText(/Checkout outcome is unresolved/)
    const original = s.calls[0]
    const requests = s.fetcher.mock.calls.length
    fireEvent.click(screen.getByText('Force mutation'))
    expect(s.fetcher.mock.calls).toHaveLength(requests)
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(true)
    view.unmount()
    const second = render(<Harness user={{ state: 'loading' }} />)
    await screen.findByText('Cart refreshed.')
    expect(screen.queryByText('Retry same checkout')).toBeNull()
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(true)
    second.rerender(<Harness user={{ state: 'ready', issuer: 'issuer', subject: 'bob' }} />)
    await waitFor(() => expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(false))
    expect(screen.queryByText('Retry same checkout')).toBeNull()
    second.rerender(<Harness />)
    await screen.findByText('Retry same checkout')
    s.checkout(async () => json(order))
    fireEvent.click(screen.getByText('Retry same checkout')); await screen.findByText('SIM-test')
    expect(s.calls[1].body).toBe(original.body)
    expect(s.calls[1].headers).toEqual(original.headers)
  })
  it('bounds checkout at eight seconds and permits only explicit same-key retry', async () => {
    const s = setup(); render(<Harness />); await review()
    s.checkout(init => new Promise((_resolve, reject) => init.signal!.addEventListener('abort', () => reject(new Error('timeout')))))
    vi.useFakeTimers()
    fireEvent.click(screen.getByText('Place simulated order'))
    await act(async () => vi.advanceTimersByTimeAsync(8000))
    expect(screen.getByText(/Checkout outcome is unresolved/)).toBeTruthy()
    expect(s.calls).toHaveLength(1)
    vi.useRealTimers(); s.checkout(async () => json(order))
    fireEvent.click(screen.getByText('Retry same checkout')); await screen.findByText('SIM-test')
    expect(s.calls[1].body).toBe(s.calls[0].body); expect(s.calls[1].headers).toEqual(s.calls[0].headers)
  })
  it('preserves attempts through session renewal', async () => {
    const s = setup(); s.checkout(async () => new Response('gateway session expired', { status: 401 }))
    const view = render(<Harness />); await place()
    await screen.findByText('Sign in to recover checkout')
    const saved = sessionStorage.getItem(key)
    view.unmount(); render(<Harness />)
    await screen.findByText('Retry same checkout')
    expect(sessionStorage.getItem(key)).toBe(saved)
    s.checkout(async () => json(order)); fireEvent.click(screen.getByText('Retry same checkout'))
    await screen.findByText('SIM-test')
  })
  it('handles unavailable storage before sending anything', async () => {
    const s = setup(); render(<Harness />); await review()
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('disabled') })
    fireEvent.click(screen.getByText('Place simulated order'))
    await screen.findByText(/Nothing was submitted/); expect(s.calls).toHaveLength(0)
  })
  it('blocks on corrupt restoration data', async () => {
    setup(); sessionStorage.setItem(key, '{bad'); render(<Harness />)
    await screen.findByText(/storage is unavailable or invalid/)
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(true)
  })
  it('requires another explicit review after changed prices', async () => {
    const s = setup(); s.checkout(async () => json({ code: 'prices_changed' }, 409, true))
    render(<Harness />); await place()
    await screen.findByText(/Checkout rejected \(prices_changed\)/)
    expect(screen.queryByText('Place simulated order')).toBeNull()
    expect(sessionStorage.getItem(key)).toBeNull()
    await review(); expect(s.calls).toHaveLength(1)
  })
  it('retains confirmation when the independent cart refresh fails', async () => {
    const s = setup(); render(<Harness />); await review()
    s.read(() => new Response(null, { status: 503 }))
    fireEvent.click(screen.getByText('Place simulated order')); await screen.findByText('SIM-test')
    await screen.findByText(/Order confirmed. Cart refresh failed/)
    expect(sessionStorage.getItem(key)).toBeNull()
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(true)
    s.read(() => json({ ...cart, items: [], subtotal_cents: 0, version: 8 }))
    fireEvent.click(screen.getByText('Refresh cart')); await screen.findByText(/Your cart is empty/)
    expect(screen.getByText('SIM-test')).toBeTruthy()
  })
  it('invalidates review after a local mutation and blocks unavailable confirmation', async () => {
    const s = setup(); render(<Harness />); await review()
    fireEvent.click(screen.getByText('Add')); await screen.findByText(/Cart changed. Review/)
    expect(screen.queryByText('Place simulated order')).toBeNull()
    s.read(() => json({ ...cart, items: [{ ...cart.items[0], product: { ...product, available: false } }] }))
    await review()
    expect((screen.getByText('Place simulated order') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByText('Back to cart'))
    expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'Your cart' }))
  })
  it.each([500, 502, 503, 504, 200])('keeps HTTP %s invalid/gateway responses unresolved even after an empty cart read', async status => {
    const s = setup(); s.checkout(async () => new Response('not an order', { status }))
    render(<Harness />); await place(); await screen.findByText(/Checkout outcome is unresolved/)
    s.read(() => json({ ...cart, items: [], subtotal_cents: 0 }))
    fireEvent.click(screen.getByText('Refresh cart')); await screen.findByText(/Your cart is empty/)
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(true)
    expect(sessionStorage.getItem(key)).not.toBeNull()
  })
  it.each([400, 403])('preserves unverified HTTP %i rejection for same-key recovery', async status => {
    const s = setup(); s.checkout(async () => json(status === 400 ? { code: 'invalid_input' } : { error: 'CSRF protection required' }, status))
    render(<Harness />); await place()
    await screen.findByText(status === 400 ? /earlier outcome remains unresolved/ : /Security check rejected this retry/)
    expect(sessionStorage.getItem(key)).not.toBeNull()
    expect(screen.queryByText('Place simulated order')).toBeNull()
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(true)
  })
  it('retains exceptional idempotency conflicts without a replacement attempt', async () => {
    const s = setup(); s.checkout(async () => json({ code: 'idempotency_conflict' }, 409))
    render(<Harness />); await place(); await screen.findByText(/Exceptional idempotency conflict/)
    expect(sessionStorage.getItem(key)).not.toBeNull()
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(true)
    expect(screen.queryByText('Place simulated order')).toBeNull()
  })
  it('invalidates review after a failed cart refresh', async () => {
    const s = setup(); render(<Harness />); await review()
    s.read(() => new Response(null, { status: 503 }))
    fireEvent.click(screen.getByText('Refresh cart')); await screen.findByText(/Cart refresh failed/)
    expect(screen.queryByText('Place simulated order')).toBeNull()
    expect(s.calls).toHaveLength(0)
  })
  it('does not release an earlier uncertain attempt following a later definitive rejection', async () => {
    const s = setup(); s.checkout(async () => { throw new Error('lost') })
    render(<Harness />); await place(); await screen.findByText(/Checkout outcome is unresolved/)
    s.checkout(async () => json({ code: 'empty_cart' }, 409))
    fireEvent.click(screen.getByText('Retry same checkout'))
    await waitFor(() => expect(s.calls).toHaveLength(2))
    expect(sessionStorage.getItem(key)).not.toBeNull()
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(true)
  })
  it('releases an uncertain attempt only after a marked service rejection, requiring renewed review', async () => {
    const s = setup(); s.checkout(async () => { throw new Error('lost') })
    render(<Harness />); await place(); await screen.findByText(/Checkout outcome is unresolved/)
    s.checkout(async () => json({ code: 'prices_changed' }, 409, true))
    fireEvent.click(screen.getByText('Retry same checkout'))
    await screen.findByText(/Checkout rejected/)
    expect(sessionStorage.getItem(key)).toBeNull()
    await waitFor(() => expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(false))
    expect(screen.queryByText('Place simulated order')).toBeNull()
    await review(); expect(s.calls).toHaveLength(2)
  })
  it.each(['confirmation', 'rejection'])('ignores a late %s after the verified identity changes', async outcome => {
    const s = setup(); let finish!: (r: Response) => void
    s.checkout(() => new Promise(resolve => { finish = resolve }))
    const view = render(<Harness />); await place()
    view.rerender(<Harness user={{ state: 'ready', issuer: 'issuer', subject: 'bob' }} />)
    await waitFor(() => expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(false))
    await act(async () => finish(outcome === 'confirmation' ? json(order) : json({ code: 'prices_changed' }, 409, true)))
    expect(screen.queryByText('SIM-test')).toBeNull()
    expect(screen.queryByText(/Checkout rejected/)).toBeNull()
    expect(sessionStorage.getItem(key)).not.toBeNull()
  })
  it.each(['null', '{}', JSON.stringify({ key: 'bad', body: '{}' })])('never submits malformed stored record %s', async raw => {
    const s = setup(); sessionStorage.setItem(key, raw); render(<Harness />)
    await screen.findByText(/storage is unavailable or invalid/)
    expect(s.calls).toHaveLength(0)
  })

  it('blocks storage read failures without checkout requests', async () => {
    const s = setup()
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('disabled') })
    render(<Harness />); await screen.findByText(/storage is unavailable or invalid/)
    fireEvent.click(screen.getByText('Force mutation'))
    expect(s.calls).toHaveLength(0)
    expect(s.fetcher.mock.calls.every(([, init]) => init.method === 'GET')).toBe(true)
  })
  it('rejects ambiguous duplicate fields in stored request bodies without sending', async () => {
    const s = setup()
    const body = reviewBody(cart).replace('"expected_cart_version":7', '"expected_cart_version":6,"expected_cart_version":7')
    sessionStorage.setItem(key, JSON.stringify({ key: crypto.randomUUID(), body }))
    render(<Harness />); await screen.findByText(/storage is unavailable or invalid/)
    expect(s.calls).toHaveLength(0)
  })
  it('keeps a marked rejected attempt locked when removing its record fails', async () => {
    const s = setup(); s.checkout(async () => { throw new Error('lost') })
    render(<Harness />); await place(); await screen.findByText(/Checkout outcome is unresolved/)
    s.checkout(async () => json({ code: 'empty_cart' }, 409, true))
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => { throw new Error('disabled') })
    fireEvent.click(screen.getByText('Retry same checkout'))
    await screen.findByText(/Could not clear rejected attempt/)
    expect(sessionStorage.getItem(key)).not.toBeNull()
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(true)
  })

})
