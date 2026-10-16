import { cents, validProduct } from './products'
import type { Product } from './products'
export type CartItem = { product: Product; quantity: number; line_total_cents: number }
export type Cart = { version: number; items: CartItem[]; subtotal_cents: number; currency: 'USD' }
export function validCart(value: unknown): value is Cart {
  if (!value || typeof value !== 'object') return false
  const c = value as Cart
  if (!cents(c.version) || c.currency !== 'USD' || !cents(c.subtotal_cents) || !Array.isArray(c.items)) return false
  const ids = new Set<string>()
  let total = 0
  for (const item of c.items) {
    if (!item || !validProduct(item.product) || !Number.isInteger(item.quantity) || item.quantity < 1 || item.quantity > 99 ||
      !cents(item.line_total_cents) || item.line_total_cents !== item.product.price_cents * item.quantity || ids.has(item.product.id)) return false
    ids.add(item.product.id)
    total += item.line_total_cents
  }
  return cents(total) && total === c.subtotal_cents
}
export class CartError extends Error {
  constructor(public status: number) { super('Cart request failed') }
}
export async function cartRequest(method = 'GET', id?: string, quantity?: number): Promise<Cart | undefined> {
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort(), 8000)
  try {
    const mutation = method !== 'GET'
    const response = await fetch(`/api/cart${mutation ? `/items${method === 'POST' ? '' : `/${encodeURIComponent(id!)}`}` : ''}`, {
      method, credentials: 'same-origin', cache: 'no-store', signal: controller.signal,
      headers: mutation ? { 'X-CSRF-Protection': '1', ...(method !== 'DELETE' ? { 'Content-Type': 'application/json' } : {}) } : undefined,
      body: method === 'POST' ? JSON.stringify({ product_id: id, quantity }) : method === 'PUT' ? JSON.stringify({ quantity }) : undefined,
    })
    if (!response.ok) throw new CartError(response.status)
    if (mutation) {
      if (response.status !== 204) throw new Error('Unexpected mutation response')
      return
    }
    const body: unknown = await response.json()
    if (!validCart(body)) throw new Error('Invalid cart response')
    return body
  } finally { window.clearTimeout(timeout) }
}
export function cartErrorMessage(error: unknown) {
  if (!(error instanceof CartError)) return 'The cart service could not be reached or returned an invalid response.'
  switch (error.status) {
    case 400: return 'Use an integer quantity from 1–99; the resulting quantity must not exceed 99.'
    case 401: return 'Your session has expired or needs renewal.'
    case 403: return 'The security check rejected this change (CSRF). Refresh the page before trying again.'
    case 404: return 'This product or cart item no longer exists. Refresh the cart.'
    case 409: return 'This product is unavailable. You can reduce its quantity or remove it.'
    default: return 'The cart service is unavailable.'
  }
}
