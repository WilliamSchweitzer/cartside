import type { Cart } from './cartApi'
import { cents } from './products'
export type Attempt = { key: string; body: string }
export type OrderItem = { product_id: string; name: string; package_label: string; quantity: number; unit_price_cents: number; line_total_cents: number }
export type Order = { id: string; confirmation: string; kind: 'simulated'; created_at: string; currency: 'USD'; total_cents: number; items: OrderItem[] }
const uuid = (s: unknown): s is string => typeof s === 'string' && /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(s)
export function reviewBody(cart: Cart) {
  return JSON.stringify({ expected_cart_version: cart.version, items: cart.items.map(i => ({ product_id: i.product.id, quantity: i.quantity, unit_price_cents: i.product.price_cents })) })
}
export function validAttempt(value: unknown): value is Attempt {
  try {
    const a = value as Attempt
    if (!a || !uuid(a.key) || typeof a.body !== 'string' || Object.keys(a).sort().join(',') !== 'body,key') return false
    const b = JSON.parse(a.body)
    const ids = new Set<string>()
    return a.body === JSON.stringify(b) && b !== null && typeof b === 'object' && Object.keys(b).sort().join(',') === 'expected_cart_version,items' && cents(b.expected_cart_version) && Array.isArray(b.items) && b.items.length > 0 && b.items.every((i: OrderItem) => {
      if (!i || Object.keys(i).sort().join(',') !== 'product_id,quantity,unit_price_cents' || !uuid(i.product_id) || ids.has(i.product_id.toLowerCase()) || !Number.isInteger(i.quantity) || i.quantity < 1 || i.quantity > 99 || !cents(i.unit_price_cents)) return false
      ids.add(i.product_id.toLowerCase()); return true
    })
  } catch { return false }
}
export function validOrder(value: unknown, attempt: Attempt): value is Order {
  const o = value as Order
  if (!o || !uuid(o.id) || typeof o.confirmation !== 'string' || !o.confirmation.startsWith('SIM-') || o.kind !== 'simulated' || o.currency !== 'USD' || typeof o.created_at !== 'string' || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(o.created_at) || !Number.isFinite(Date.parse(o.created_at)) || !cents(o.total_cents) || !Array.isArray(o.items)) return false
  const requested = JSON.parse(attempt.body).items as OrderItem[]
  const remaining = new Map(requested.map(i => [i.product_id.toLowerCase(), i]))
  let total = 0
  for (const i of o.items) {
    if (!i || !uuid(i.product_id)) return false
    const r = remaining.get(i.product_id.toLowerCase())
    if (!r || i.quantity !== r.quantity || i.unit_price_cents !== r.unit_price_cents || typeof i.name !== 'string' || !i.name || typeof i.package_label !== 'string' || !cents(i.line_total_cents) || i.line_total_cents !== i.quantity * i.unit_price_cents) return false
    remaining.delete(i.product_id.toLowerCase()); total += i.line_total_cents
  }
  return remaining.size === 0 && cents(total) && total === o.total_cents
}
export class CheckoutError extends Error {
  constructor(public status: number, public code: string, public noMatchingOrder = false) { super('Checkout rejected') }
}
export async function submitCheckout(attempt: Attempt): Promise<Order> {
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort(), 8000)
  try {
    const response = await fetch('/api/checkout', { method: 'POST', credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Protection': '1', 'Idempotency-Key': attempt.key }, body: attempt.body })
    if (response.status !== 200) {
      let code = ''
      try { code = (await response.json()).code } catch { /* Gateway responses need not be JSON. */ }
      throw new CheckoutError(response.status, code,
        response.status === 409 && response.headers.get('X-Cartside-Checkout-Rejection') === 'no-matching-order' &&
        response.headers.get('Content-Type')?.split(';')[0] === 'application/json')
    }
    const body: unknown = await response.json()
    if (!validOrder(body, attempt)) throw new Error('Invalid order confirmation')
    return body
  } finally { window.clearTimeout(timeout) }
}
