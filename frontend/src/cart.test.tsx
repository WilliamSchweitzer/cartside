import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App'
import { cartRequest, validCart } from './cartApi'
import { useCart } from './useCart'
import CartView from './Cart'
const product = { id: 'bb5ce88f-908c-5a99-8ae7-90b60b00ae1b', name: 'Apples', package_label: '3 lb bag', image_reference: '', category: 'produce', price_cents: 125, currency: 'USD', available: true }
const cart = (quantity = 0, available = true) => ({ version: quantity, currency: 'USD', subtotal_cents: quantity * 125, items: quantity ? [{ product: { ...product, available }, quantity, line_total_cents: quantity * 125 }] : [] })
const json = (value: unknown) => new Response(JSON.stringify(value), { status: 200 })
function setup(initial = 0, available = true, catalogAvailable = available) {
  let quantity = initial
  const calls: RequestInit[] = []
  const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
    if (url.startsWith('/api/products')) {
      const offset = Number(new URL(url, 'http://localhost').searchParams.get('offset'))
      return json({ items: [{ ...product, available: catalogAvailable }], limit: 20, offset, has_more: offset === 0 })
    }
    if (url === '/api/me') return json({ subject: 'test', issuer: 'test' })
    if (url === '/api/health/ready') return json({ status: 'ok' })
    if (init?.method !== 'GET') {
      calls.push(init!)
      quantity = init?.method === 'POST' ? quantity + 1 : init?.method === 'DELETE' ? 0 : JSON.parse(init!.body as string).quantity
      return new Response(null, { status: 204 })
    }
    return json(cart(quantity, available))
  })
  vi.stubGlobal('fetch', fetcher)
  return { fetcher, calls }
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.useRealTimers() })
async function loaded() { await screen.findByText('Cart refreshed.') }
function Harness() { const state = useCart(); return <><button disabled={state.busy || state.blocked} onClick={() => void state.mutate('POST', product.id, product.name, 1)}>Add</button><CartView state={state} /></> }
describe('storefront cart', () => {
  it('adds, updates absolutely, removes, displays cents and preserves search and input focus', async () => {
    const { calls } = setup()
    render(<App />); await loaded()
    fireEvent.change(screen.getByLabelText('Search groceries'), { target: { value: 'draft search' } })
    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    await screen.findByText('Page 2')
    fireEvent.click(screen.getByRole('button', { name: 'Add Apples to cart' }))
    await screen.findByText('Subtotal: $1.25')
    const input = screen.getByLabelText('Quantity for Apples')
    input.focus()
    fireEvent.change(input, { target: { value: '3' } })
    fireEvent.submit(input.closest('form')!)
    await screen.findByText('Subtotal: $3.75')
    expect(document.activeElement).toBe(input)
    expect(screen.getByText('Page 2')).toBeTruthy()
    expect(screen.getByText('3 total units · 1 distinct items')).toBeTruthy()
    expect((screen.getByLabelText('Search groceries') as HTMLInputElement).value).toBe('draft search')
    fireEvent.click(screen.getByRole('button', { name: 'Remove Apples' }))
    await screen.findByText(/Your cart is empty/)
    expect(calls.map(c => c.method)).toEqual(['POST', 'PUT', 'DELETE'])
    expect(JSON.parse(calls[0].body as string)).toEqual({ product_id: product.id, quantity: 1 })
    expect(JSON.parse(calls[1].body as string)).toEqual({ quantity: 3 })
    for (const call of calls) {
      expect(call.credentials).toBe('same-origin')
      expect(call.headers).toMatchObject({ 'X-CSRF-Protection': '1' })
      expect(call.headers).not.toHaveProperty('Origin')
      expect(call.headers).not.toHaveProperty('Sec-Fetch-Site')
    }
    expect(calls[0].headers).toMatchObject({ 'Content-Type': 'application/json' })
    expect(calls[1].headers).toMatchObject({ 'Content-Type': 'application/json' })
  })
  it('keeps unavailable items visible, prevents increases and invalid quantities, allows reductions', async () => {
    const { calls } = setup(3, false, true); render(<App />); await loaded()
    expect((screen.getByRole('button', { name: 'Add Apples to cart' }) as HTMLButtonElement).disabled).toBe(true)
    const input = screen.getByLabelText('Quantity for Apples')
    for (const value of ['4', '0', '100', '1.5']) {
      fireEvent.change(input, { target: { value } }); fireEvent.submit(input.closest('form')!)
    }
    expect(calls).toHaveLength(0)
    fireEvent.change(input, { target: { value: '2' } }); fireEvent.submit(input.closest('form')!)
    await screen.findByText('Subtotal: $2.50')
    expect(screen.getByText(/Subtotal includes unavailable items/)).toBeTruthy()
  })
  it('guards rapid clicks synchronously and serializes through the follow-up read', async () => {
    const { fetcher, calls } = setup(); render(<Harness />); await loaded()
    let finish!: (r: Response) => void
    fetcher.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const button = screen.getByText('Add')
    act(() => { fireEvent.click(button); fireEvent.click(button) })
    expect(fetcher.mock.calls.filter(([, i]) => i?.method === 'POST')).toHaveLength(1)
    expect((button as HTMLButtonElement).disabled).toBe(true)
    await act(async () => finish(new Response(null, { status: 204 })))
    await screen.findByText('Added one Apples to cart.')
    expect(calls).toHaveLength(0)
  })
  it('reports saved changes with failed refresh and locks until a read-only refresh succeeds', async () => {
    const { fetcher } = setup(); render(<Harness />); await loaded()
    fetcher.mockResolvedValueOnce(new Response(null, { status: 204 })).mockResolvedValueOnce(new Response(null, { status: 503 }))
    fireEvent.click(screen.getByText('Add'))
    await screen.findByText(/Change saved, cart refresh failed/)
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByText('Refresh cart')); await loaded()
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(false)
  })
  it('reconciles uncertain network outcomes without retrying mutations; failed reconciliation blocks edits', async () => {
    const { fetcher } = setup(); render(<Harness />); await loaded()
    fetcher.mockRejectedValueOnce(new TypeError('network')).mockResolvedValueOnce(new Response(null, { status: 503 }))
    fireEvent.click(screen.getByText('Add'))
    await screen.findByText(/outcome is uncertain.*Cart refresh failed/)
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(true)
    expect(fetcher.mock.calls.filter(([, i]) => i?.method === 'POST')).toHaveLength(1)
    fetcher.mockResolvedValueOnce(json(cart(1)))
    fireEvent.click(screen.getByText('Refresh cart')); await screen.findByText('Subtotal: $1.25')
    expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(false)
  })
  it('refreshes after an uncertain response even when the addition committed', async () => {
    const { fetcher } = setup(); render(<Harness />); await loaded()
    fetcher.mockRejectedValueOnce(new Error('connection lost')).mockResolvedValueOnce(json(cart(1)))
    fireEvent.click(screen.getByText('Add')); await screen.findByText('Subtotal: $1.25')
    expect(screen.getByText(/outcome is uncertain/)).toBeTruthy()
  })
  it.each([400, 401, 403, 404, 409, 503])('handles mutation HTTP %i', async status => {
    const { fetcher } = setup(); render(<Harness />); await loaded()
    fetcher.mockResolvedValueOnce(new Response(null, { status }))
    fireEvent.click(screen.getByText('Add'))
    await screen.findByText(status >= 500 ? /outcome is uncertain/ : /Change failed/)
    if (status === 401) { expect(screen.getByText('Return to sign-in')).toBeTruthy(); expect((screen.getByText('Add') as HTMLButtonElement).disabled).toBe(true) }
    if (status === 403) expect(screen.getByText(/CSRF/)).toBeTruthy()
  })
  it('ignores obsolete reads after effect cleanup and rejects malformed shapes', async () => {
    let old!: (r: Response) => void
    const fetcher = vi.fn().mockImplementationOnce(() => new Promise(resolve => { old = resolve })).mockResolvedValue(json(cart(2)))
    vi.stubGlobal('fetch', fetcher)
    const first = render(<Harness />)
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1))
    first.unmount()
    render(<Harness />); await screen.findByText('Subtotal: $2.50')
    await act(async () => old(json(cart(1))))
    expect(screen.queryByText('Subtotal: $1.25')).toBeNull()
    expect(validCart({ ...cart(1), subtotal_cents: 2 })).toBe(false)
    expect(validCart({ ...cart(1), items: [null] })).toBe(false)
  })
  it('distinguishes initial failed reads from empty carts', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 503 })))
    render(<Harness />)
    await waitFor(() => expect(screen.getByText(/Cart refresh failed/)).toBeTruthy())
    expect(screen.queryByText(/Your cart is empty/)).toBeNull()
  })
  it('prevents a superseded read in the same cart state from replacing newer data', async () => {
    let old!: (r: Response) => void
    const fetcher = vi.fn().mockImplementationOnce(() => new Promise(resolve => { old = resolve })).mockResolvedValue(json(cart(2)))
    vi.stubGlobal('fetch', fetcher)
    function RefreshHarness() {
      const state = useCart()
      return <><button onClick={() => void state.refresh()}>Force read</button><CartView state={state} /></>
    }
    render(<RefreshHarness />)
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1))
    fireEvent.click(screen.getByText('Force read'))
    await screen.findByText('Subtotal: $2.50')
    await act(async () => old(json(cart(1))))
    expect(screen.queryByText('Subtotal: $1.25')).toBeNull()
  })
  it('bounds requests with an eight-second timeout', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn((_url: string, init: RequestInit) => new Promise((_resolve, reject) => {
      init.signal!.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')))
    })))
    const request = cartRequest()
    const assertion = expect(request).rejects.toThrow('Aborted')
    await vi.advanceTimersByTimeAsync(8000)
    await assertion
  })

  it('disables unavailable catalog additions even before an item is in the cart', async () => {
    setup(0, false)
    render(<App />); await loaded()
    expect((screen.getByRole('button', { name: 'Add Apples to cart' }) as HTMLButtonElement).disabled).toBe(true)
  })

})
