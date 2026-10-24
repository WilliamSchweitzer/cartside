import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Assistant, { historyLimit } from './Assistant'
import { assistantRequest, boundedText, retrySeconds, utf8Bytes, validAssistantResult } from './assistantApi'
import { useCart } from './useCart'
import type { CartState } from './useCart'
import Cart from './Cart'
import Checkout, { storageKey } from './Checkout'
import type { Identity } from './App'

const product = { id: 'bb5ce88f-908c-5a99-8ae7-90b60b00ae1b', name: 'Apples', package_label: 'bag', price_cents: 125, currency: 'USD' as const, available: true, category: 'produce', image_reference: '' }
const suggestion = { product_id: product.id, name: product.name, package_label: product.package_label, price_cents: 125, currency: 'USD', quantity: 3, reason: 'Demo candidate.', available: true }
const result = { version: 1, provider: 'simulated: cartside-mock-v1', text: 'SIMULATED: Demo suggestions.', suggestions: [suggestion] }
const identity: Identity = { state: 'ready', issuer: 'issuer', subject: 'alice' }
const json = (body: unknown, status = 200, headers = {}) => new Response(JSON.stringify(body), { status, headers })
const cart = (quantity = 0) => ({ version: quantity, currency: 'USD', subtotal_cents: quantity * 125, items: quantity ? [{ product, quantity, line_total_cents: quantity * 125 }] : [] })
function Harness({ user = identity }: { user?: Identity }) {
  const state = useCart(true)
  return <><Assistant state={state} identity={user} /><Cart state={state} /><Checkout state={state} identity={user} /></>
}
function setup(initial = 0) {
  let quantity = initial
  const messages: RequestInit[] = [], additions: RequestInit[] = []
  let assistant: (init: RequestInit) => Promise<Response> = async () => json(result)
  let mutation: (init: RequestInit) => Promise<Response> = async init => { quantity += JSON.parse(init.body as string).quantity; return new Response(null, { status: 204 }) }
  let read: () => Promise<Response> = async () => json(cart(quantity))
  let checkout: () => Promise<Response> = async () => { throw new Error('lost') }
  const fetcher = vi.fn(async (url: string, init: RequestInit) => {
    if (url === '/api/assistant/messages') { messages.push(init); return assistant(init) }
    if (url === '/api/checkout') return checkout()
    if (init.method === 'POST') { additions.push(init); return mutation(init) }
    if (init.method === 'PUT') { quantity = JSON.parse(init.body as string).quantity; return new Response(null, { status: 204 }) }
    return read()
  })
  vi.stubGlobal('fetch', fetcher)
  return { messages, additions, fetcher, assistant: (f: typeof assistant) => { assistant = f }, mutation: (f: typeof mutation) => { mutation = f }, read: (f: typeof read) => { read = f }, checkout: (f: typeof checkout) => { checkout = f } }
}
async function loaded() { await screen.findByText('Cart refreshed.') }
const sendButton = () => screen.getByRole('button', { name: 'Send message' }) as HTMLButtonElement
async function send(prompt = 'breakfast for two') {
  fireEvent.change(screen.getByLabelText('Message to demo assistant'), { target: { value: prompt } })
  fireEvent.click(sendButton())
  await waitFor(() => expect(sendButton().disabled).toBe(false))
}
const accept = () => screen.getByRole('button', { name: 'Accept Apples' }) as HTMLButtonElement
const decline = () => screen.getByRole('button', { name: 'Decline Apples' }) as HTMLButtonElement
afterEach(() => { cleanup(); sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.useRealTimers() })

describe('assistant API contract', () => {
  it('counts UTF-8 bytes, including astral characters, and rejects lone surrogates', () => {
    expect(utf8Bytes('é😀')).toBe(6)
    expect(boundedText('é'.repeat(128), 256)).toBe(true)
    expect(boundedText('é'.repeat(129), 256)).toBe(false)
    expect(boundedText('😀'.repeat(64), 256)).toBe(true)
    for (const text of ['', ' \n', '\u0085', '\u3000', '\ud800', '\udc00']) expect(boundedText(text, 256)).toBe(false)
  })
  it('validates version, provider, all suggestion fields, quantity bounds and duplicate products', () => {
    expect(validAssistantResult(result)).toBe(true)
    expect(validAssistantResult({ ...result, suggestions: [] })).toBe(true)
    for (const invalid of [null, {}, { ...result, version: 2 }, { ...result, provider: 'live' }, { ...result, text: '' }, { ...result, text: 'x'.repeat(401) }, { ...result, suggestions: null }, { ...result, suggestions: [suggestion, suggestion] }, { ...result, suggestions: Array(6).fill(suggestion) }]) expect(validAssistantResult(invalid)).toBe(false)
    for (const change of [{ product_id: 'bad' }, { name: null }, { package_label: '' }, { reason: 'x'.repeat(121) }, { quantity: 0 }, { quantity: 100 }, { quantity: 1.5 }, { price_cents: -1 }, { price_cents: 1.5 }, { currency: 'EUR' }, { available: 'yes' }]) expect(validAssistantResult({ ...result, suggestions: [{ ...suggestion, ...change }] })).toBe(false)
  })
  it('constructs a current-message-only, same-origin CSRF request', async () => {
    const s = setup(); const signal = new AbortController().signal
    await assistantRequest('breakfast for two', signal)
    expect(s.messages).toHaveLength(1)
    expect(s.fetcher.mock.calls[0][0]).toBe('/api/assistant/messages')
    expect(s.messages[0]).toEqual({ method: 'POST', credentials: 'same-origin', cache: 'no-store', signal, headers: { 'Content-Type': 'application/json', 'X-CSRF-Protection': '1' }, body: '{"message":"breakfast for two"}' })
    await expect(assistantRequest('é'.repeat(129), signal)).rejects.toThrow(/256/)
    expect(s.messages).toHaveLength(1)
  })
  it.each([
    [503, 'assistant_unavailable', /disabled/], [401, '', /session needs renewal/], [403, '', /CSRF/],
    [429, 'assistant_rate_limited', /Your demo assistant request limit/], [429, 'provider_rate_limited', /provider request limit/], [429, 'assistant_busy', /already running/], [429, 'assistant_capacity', /capacity/],
    [504, 'timeout', /timed out/], [409, 'catalog_changed', /products changed/], [503, 'catalog_unavailable', /catalog could not/], [502, 'invalid_provider_output', /invalid response/], [502, 'provider_unavailable', /provider is unavailable/],
  ] as const)('maps HTTP %s %s to safe messages', async (status, code, message) => {
    const s = setup(); s.assistant(async () => json({ code, message: '<secret upstream body>' }, status))
    await expect(assistantRequest('x', new AbortController().signal)).rejects.toThrow(message)
  })
  it('rejects malformed successful output and parses numeric/date Retry-After', async () => {
    const s = setup(); s.assistant(async () => new Response('<html>', { status: 200 }))
    await expect(assistantRequest('x', new AbortController().signal)).rejects.toThrow(/invalid response/)
    expect(retrySeconds('10')).toBe(10)
    expect(retrySeconds('Thu, 01 Jan 1970 00:01:00 GMT', 55000)).toBe(5)
    expect(retrySeconds('bad')).toBe(0)
    expect(retrySeconds(null)).toBe(0)
  })
})

describe('demo assistant panel and shared cart', () => {
  it('labels the demo, renders text safely, offers examples, and never mutates for suggestions or Decline', async () => {
    const s = setup(); s.assistant(async () => json({ ...result, text: '<img src=x onerror=alert(1)>', suggestions: [{ ...suggestion, name: '<b>Apples</b>' }] }))
    render(<Harness />); await loaded()
    expect(screen.getByRole('region', { name: 'Demo shopping assistant' })).toBeTruthy()
    expect(screen.getByText(/not a live language model/)).toBeTruthy()
    expect(screen.getByText(/Dietary and allergen/)).toBeTruthy()
    for (const prompt of ['breakfast for two', 'vegetarian dinner', 'snacks for a picnic']) expect(screen.getByRole('button', { name: prompt })).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'vegetarian dinner' }))
    expect((screen.getByLabelText('Message to demo assistant') as HTMLTextAreaElement).value).toBe('vegetarian dinner')
    const input = screen.getByLabelText('Message to demo assistant'); input.focus()
    await send('<script>hello</script>')
    expect(document.activeElement).toBe(input)
    expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeTruthy()
    expect(document.querySelector('.assistant img, .assistant script, .assistant b')).toBeNull()
    expect(screen.getByText(/bag · Unit price: \$1.25 · Quantity: 3/)).toBeTruthy()
    expect(s.additions).toHaveLength(0)
    fireEvent.click(screen.getByRole('button', { name: 'Decline <b>Apples</b>' }))
    expect(screen.getByText('Declined. Nothing was added.')).toBeTruthy()
    expect(s.additions).toHaveLength(0)
  })
  it('enforces byte limits and prevents concurrent message sends synchronously', async () => {
    const s = setup(); let finish!: (r: Response) => void
    s.assistant(() => new Promise(resolve => { finish = resolve }))
    render(<Harness />); await loaded()
    const input = screen.getByLabelText('Message to demo assistant')
    fireEvent.change(input, { target: { value: 'é'.repeat(129) } }); expect(sendButton().disabled).toBe(true)
    expect(screen.getByText(/258 \/ 256/)).toBeTruthy()
    fireEvent.change(input, { target: { value: 'é'.repeat(128) } })
    act(() => { fireEvent.click(sendButton()); fireEvent.submit(input.closest('form')!) })
    expect(s.messages).toHaveLength(1); expect(sendButton().disabled).toBe(true)
    await act(async () => finish(json(result)))
    expect(sendButton().disabled).toBe(false)
  })
  it('adds the suggested quantity once and marks accepted only after confirmation', async () => {
    const s = setup(); let finish!: (r: Response) => void
    s.mutation(() => new Promise(resolve => { finish = resolve }))
    render(<Harness />); await loaded(); await send()
    act(() => { fireEvent.click(accept()); fireEvent.click(accept()) })
    expect(s.additions).toHaveLength(1)
    expect(JSON.parse(s.additions[0].body as string)).toEqual({ product_id: product.id, quantity: 3 })
    expect(screen.queryByText('Accepted. Added to your cart.')).toBeNull()
    expect(accept().disabled).toBe(true); expect(decline().disabled).toBe(true)
    await act(async () => finish(new Response(null, { status: 204 })))
    await screen.findByText('Accepted. Added to your cart.')
    expect(screen.getByText('Added 3 Apples to cart.')).toBeTruthy()
    fireEvent.click(accept()); expect(s.additions).toHaveLength(1)
  })
  it('marks accepted after confirmed mutation even when refresh fails and keeps it accepted after recovery', async () => {
    const s = setup(); render(<Harness />); await loaded(); await send()
    s.read(async () => new Response(null, { status: 503 }))
    fireEvent.click(accept()); await screen.findByText(/Accepted. Change saved; cart refresh failed/)
    expect(accept().disabled).toBe(true)
    s.read(async () => json(cart(3)))
    fireEvent.click(screen.getByText('Refresh cart')); await screen.findByText('Subtotal: $3.75')
    expect(accept().disabled).toBe(true); expect(s.additions).toHaveLength(1)
  })
  it('permits deliberate retry after definitive rejection', async () => {
    const s = setup(); s.mutation(async () => new Response(null, { status: 409 }))
    render(<Harness />); await loaded(); await send(); fireEvent.click(accept())
    await waitFor(() => expect(accept().disabled).toBe(false))
    expect(screen.getAllByText(/This product is unavailable/).length).toBeGreaterThan(0)
    expect(s.additions).toHaveLength(1)
    s.mutation(async () => new Response(null, { status: 204 }))
    fireEvent.click(accept()); await screen.findByText('Accepted. Added to your cart.')
    expect(s.additions).toHaveLength(2)
  })
  it.each(['network', 'server', 'invalid-success'])('retains uncertain %s additions across successful read-only reconciliation without retry', async failure => {
    const s = setup(); s.mutation(async () => { if (failure === 'network') throw new Error('lost'); return new Response(null, { status: failure === 'server' ? 503 : 200 }) })
    render(<Harness />); await loaded(); await send(); fireEvent.click(accept())
    await screen.findByText(/Outcome uncertain. Inspect/)
    expect(accept().disabled).toBe(true); expect(decline().disabled).toBe(true)
    s.read(async () => json(cart(3))); fireEvent.click(screen.getByText('Refresh cart')); await screen.findByText('Subtotal: $3.75')
    expect(accept().disabled).toBe(true)
    fireEvent.click(accept()); expect(s.additions).toHaveLength(1)
  })
  it('honors visible cooldown, never retries automatically, and lets the user deliberately resend the same message', async () => {
    const s = setup(); render(<Harness />); await loaded()
    s.assistant(async () => json({ code: 'assistant_busy' }, 429, { 'Retry-After': '2' }))
    vi.useFakeTimers()
    fireEvent.change(screen.getByLabelText('Message to demo assistant'), { target: { value: 'breakfast for two' } })
    await act(async () => fireEvent.click(sendButton()))
    expect(screen.getByText('Try again in 2 seconds.')).toBeTruthy(); expect(sendButton().disabled).toBe(true)
    await act(async () => vi.advanceTimersByTimeAsync(2000))
    expect(sendButton().disabled).toBe(false); expect(s.messages).toHaveLength(1)
    s.assistant(async () => json(result)); await act(async () => fireEvent.click(sendButton()))
    expect(s.messages).toHaveLength(2); expect(s.messages[1].body).toBe(s.messages[0].body)
  })
  it('bounds a stalled request, then requires a deliberate resend', async () => {
    const s = setup(); render(<Harness />); await loaded()
    s.assistant(init => new Promise((_resolve, reject) => init.signal!.addEventListener('abort', () => reject(new Error('timeout')))))
    vi.useFakeTimers()
    fireEvent.change(screen.getByLabelText('Message to demo assistant'), { target: { value: 'x' } }); fireEvent.click(sendButton())
    await act(async () => vi.advanceTimersByTimeAsync(8000))
    expect(sendButton().disabled).toBe(false); expect(s.messages).toHaveLength(1)
    expect(screen.getAllByText(/request timed out/).length).toBeGreaterThan(0)
  })
  it('ignores late responses after identity changes and aborts unmounted work', async () => {
    const s = setup(); let finish!: (r: Response) => void
    s.assistant(() => new Promise(resolve => { finish = resolve }))
    const view = render(<Harness />); await loaded()
    fireEvent.change(screen.getByLabelText('Message to demo assistant'), { target: { value: 'private old prompt' } }); fireEvent.click(sendButton())
    view.rerender(<Harness user={{ state: 'ready', issuer: 'issuer', subject: 'bob' }} />)
    expect(s.messages[0].signal!.aborted).toBe(true)
    await act(async () => finish(json(result)))
    expect(screen.queryByText(/private old prompt/)).toBeNull(); expect(screen.queryByText(result.text)).toBeNull()
    view.unmount()
  })
  it('keeps shopping usable when assistant is disabled or returns malformed output', async () => {
    const s = setup(); s.assistant(async () => json({ code: 'assistant_unavailable' }, 503))
    render(<Harness />); await loaded(); await send()
    expect(screen.getAllByText(/assistant is disabled/).length).toBeGreaterThan(0)
    expect((screen.getByText('Refresh cart') as HTMLButtonElement).disabled).toBe(false)
    s.assistant(async () => json({ ...result, suggestions: [{ ...suggestion, quantity: 999 }] })); await send()
    expect(screen.queryByRole('button', { name: 'Accept Apples' })).toBeNull()
    expect(s.additions).toHaveLength(0)
  })
  it.each(['assistant_unavailable', 'provider_unavailable', 'invalid_provider_output'])('allows manual edits and explicit checkout after %s', async code => {
    const s = setup(1)
    s.assistant(async () => json({ code }, code === 'assistant_unavailable' ? 503 : 502))
    s.checkout(async () => {
      s.read(async () => json(cart()))
      return json({ id: product.id, confirmation: 'SIM-manual', kind: 'simulated', created_at: '2026-10-23T12:00:00Z', currency: 'USD', total_cents: 250,
        items: [{ product_id: product.id, name: product.name, package_label: product.package_label, quantity: 2, unit_price_cents: 125, line_total_cents: 250 }] })
    })
    render(<Harness />); await loaded(); await send()
    s.read(async () => json(cart(2)))
    fireEvent.change(screen.getByLabelText('Quantity for Apples'), { target: { value: '2' } })
    fireEvent.click(screen.getByRole('button', { name: 'Update quantity for Apples' }))
    await screen.findByText('Subtotal: $2.50')
    fireEvent.click(screen.getByText('Review simulated order')); await screen.findByText('Place simulated order')
    expect(s.fetcher.mock.calls.filter(([url]) => url === '/api/checkout')).toHaveLength(0)
    fireEvent.click(screen.getByText('Place simulated order'))
    await screen.findByText('SIM-manual')
    await screen.findByText(/Your cart is empty/)
    expect(screen.queryByRole('button', { name: 'Accept Apples' })).toBeNull()
  })
  it('invalidates an existing checkout review when an accepted suggestion changes the cart', async () => {
    const s = setup(1); render(<Harness />); await loaded(); await send()
    fireEvent.click(screen.getByText('Review simulated order')); await screen.findByText('Place simulated order')
    fireEvent.click(accept()); await screen.findByText('Accepted. Added to your cart.')
    expect(screen.queryByText('Place simulated order')).toBeNull()
    expect(screen.getByText(/Cart changed. Review/)).toBeTruthy()
    expect(screen.getByText('Subtotal: $5.00')).toBeTruthy(); expect(s.additions).toHaveLength(1)
  })
  it('disables acceptance throughout checkout submission and unresolved recovery, including after a cart GET', async () => {
    const s = setup(1); let fail!: () => void
    s.checkout(() => new Promise((_resolve, reject) => { fail = () => reject(new Error('lost')) }))
    render(<Harness />); await loaded(); await send()
    fireEvent.click(screen.getByText('Review simulated order')); await screen.findByText('Place simulated order')
    fireEvent.click(screen.getByText('Place simulated order')); expect(accept().disabled).toBe(true)
    fireEvent.click(accept()); expect(s.additions).toHaveLength(0)
    await act(async () => fail()); await screen.findByText(/Checkout outcome is unresolved/)
    fireEvent.click(screen.getByText('Refresh cart')); await loaded()
    expect(accept().disabled).toBe(true); expect(s.additions).toHaveLength(0)
  })
  it('blocks acceptance for restored unresolved checkout', async () => {
    const s = setup(1)
    sessionStorage.setItem(storageKey('issuer', 'alice'), JSON.stringify({ key: crypto.randomUUID(), body: JSON.stringify({ expected_cart_version: 1, items: [{ product_id: product.id, quantity: 1, unit_price_cents: 125 }] }) }))
    render(<Harness />); await loaded(); await send()
    expect(accept().disabled).toBe(true); fireEvent.click(accept()); expect(s.additions).toHaveLength(0)
  })
  it('bounds history while preserving an uncertain addition', async () => {
    const s = setup(); s.mutation(async () => { throw new Error('lost') })
    render(<Harness />); await loaded(); await send('retained prompt'); fireEvent.click(accept())
    await screen.findByText(/Outcome uncertain. Inspect/)
    for (let i = 0; i < historyLimit + 2; i++) await send(`prompt-${i}`)
    const list = screen.getByRole('list', { name: 'Assistant messages' })
    expect(list.children).toHaveLength(historyLimit)
    expect(within(list).getByText(/retained prompt/)).toBeTruthy()
    expect(within(list).queryByText(/You: prompt-0$/)).toBeNull()
    expect(screen.getByText(/Outcome uncertain. Inspect/)).toBeTruthy()
  })
  it('preserves a pending addition while later message exchanges are evicted', async () => {
    const s = setup(); let finish!: (r: Response) => void
    s.mutation(() => new Promise(resolve => { finish = resolve }))
    render(<Harness />); await loaded(); await send('pending original'); fireEvent.click(accept())
    for (let i = 0; i < historyLimit + 1; i++) await send(`later-${i}`)
    const list = screen.getByRole('list', { name: 'Assistant messages' })
    expect(list.children).toHaveLength(historyLimit)
    expect(within(list).getByText(/pending original/)).toBeTruthy()
    expect(screen.getByText('Adding suggestion…')).toBeTruthy()
    await act(async () => finish(new Response(null, { status: 204 })))
    expect(screen.getByText('Accepted. Added to your cart.')).toBeTruthy()
  })
  it('fails closed for new messages when every retained exchange has an uncertain addition', async () => {
    const s = setup(); s.mutation(async () => { throw new Error('lost') })
    render(<Harness />); await loaded()
    for (let i = 0; i < historyLimit; i++) {
      await send(`uncertain-${i}`)
      const list = screen.getByRole('list', { name: 'Assistant messages' })
      const entry = list.lastElementChild as HTMLElement
      act(() => {
        fireEvent.click(within(entry).getByRole('button', { name: 'Accept Apples' }))
        // A batched send must already see the last addition as protected.
        if (i === historyLimit - 1) fireEvent.submit(screen.getByLabelText('Message to demo assistant').closest('form')!)
      })
      await within(entry).findByText(/Outcome uncertain. Inspect/)
    }
    expect(sendButton().disabled).toBe(true)
    expect(screen.getByText(/History is full of unresolved additions/)).toBeTruthy()
    fireEvent.submit(screen.getByLabelText('Message to demo assistant').closest('form')!)
    expect(s.messages).toHaveLength(historyLimit)
    expect(screen.getByRole('list', { name: 'Assistant messages' }).children).toHaveLength(historyLimit)
  })
  it('disables acceptance during cart loading, cart mutations, and failed reconciliation', async () => {
    const mutate = vi.fn()
    const state = { busy: true, blocked: false, mutate } as unknown as CartState
    setup(); const view = render(<Assistant state={state} identity={identity} />); await send()
    expect(accept().disabled).toBe(true); fireEvent.click(accept()); expect(mutate).not.toHaveBeenCalled()
    view.rerender(<Assistant state={{ ...state, busy: false, blocked: true }} identity={identity} />)
    expect(accept().disabled).toBe(true); fireEvent.click(accept()); expect(mutate).not.toHaveBeenCalled()
    view.rerender(<Assistant state={{ ...state, busy: false, blocked: false }} identity={identity} />)
    expect(accept().disabled).toBe(false)
  })
  it('does not mark a not-started mutation accepted', async () => {
    const mutate = vi.fn().mockResolvedValue({ outcome: 'not-started' })
    const state = { busy: false, blocked: false, mutate } as unknown as CartState
    const s = setup(); render(<Assistant state={state} identity={identity} />); await send(); fireEvent.click(accept())
    await screen.findAllByText(/Addition did not start/)
    expect(accept().disabled).toBe(false); expect(s.additions).toHaveLength(0)
  })
})
