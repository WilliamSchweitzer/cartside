import { useEffect, useRef, useState } from 'react'
import type { Identity } from './App'
import type { Cart } from './cartApi'
import type { CartState } from './useCart'
import { CheckoutError, reviewBody, submitCheckout, validAttempt } from './checkoutApi'
import type { Attempt, Order } from './checkoutApi'
import { usd } from './products'

export const storageKey = (issuer: string, subject: string) => `cartside.checkout.v1:${JSON.stringify([issuer, subject])}`
const conflicts = new Set(['empty_cart', 'stale_cart_version', 'reviewed_items_mismatch', 'products_unavailable', 'prices_changed'])
export default function Checkout({ state, identity }: { state: CartState; identity: Identity }) {
  const [scope, setScope] = useState('')
  const [review, setReview] = useState<{ snapshot: Cart; source: Cart }>()
  const [pending, setPending] = useState<Attempt>()
  const [order, setOrder] = useState<Order>()
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const [renew, setRenew] = useState(false)
  const guard = useRef(false)
  const heading = useRef<HTMLHeadingElement>(null)
  const identityScope = identity.state === 'ready' ? storageKey(identity.issuer, identity.subject) : ''
  useEffect(() => {
    if (!identityScope || identityScope === scope) return
    // Restoration is read-only and happens only after /api/me verifies ownership.
    const restore = () => {
      setOrder(undefined); setReview(undefined); setPending(undefined); setScope(identityScope)
      try {
        const raw = sessionStorage.getItem(identityScope)
        if (raw !== null) {
          const value: unknown = JSON.parse(raw)
          if (!validAttempt(value)) throw new Error('Invalid recovery data')
          setPending(value); state.lockCheckout()
          setMessage('An unresolved simulated checkout was restored. Retry the same checkout to learn its outcome.')
        } else { state.unlockCheckout(); setMessage('') }
      } catch { state.lockCheckout(); setMessage('Checkout recovery storage is unavailable or invalid. Checkout and cart changes are blocked; restore access to this tab’s sessionStorage and reload.') }
    }
    void Promise.resolve().then(restore)
  }, [identityScope, scope, state])
  useEffect(() => { if (review || pending || order) heading.current?.focus() }, [review, pending, order])
  const currentReview = review && !state.blocked && state.cart === review.source ? review.snapshot : undefined
  async function startReview() {
    if (guard.current || pending || !state.lockCheckout()) return
    guard.current = true; setBusy(true); setOrder(undefined); setMessage('')
    try {
      const cart = await state.refresh()
      if (cart && cart.items.length) setReview({ snapshot: structuredClone(cart), source: cart })
      else setMessage('A nonempty, successfully refreshed cart is required for review.')
    } finally { state.unlockCheckout(); guard.current = false; setBusy(false) }
  }
  async function submit(existing?: Attempt) {
    if (guard.current || scope !== identityScope || !identityScope || (!existing && (!currentReview || state.busy || state.blocked)) || !state.lockCheckout()) return
    guard.current = true; setBusy(true); setRenew(false)
    let attempt = existing
    try {
      if (!attempt) {
        if (currentReview!.items.some(i => !i.product.available)) return
        attempt = { key: crypto.randomUUID(), body: reviewBody(currentReview!) }
        try {
          sessionStorage.setItem(scope, JSON.stringify(attempt))
          if (sessionStorage.getItem(scope) !== JSON.stringify(attempt)) throw new Error('Storage failed')
        } catch {
          setMessage('Recovery storage is unavailable. Nothing was submitted. Enable sessionStorage before placing a simulated order.'); state.unlockCheckout(); return
        }
        setPending(attempt)
      }
      const result = await submitCheckout(attempt)
      setOrder(result); setPending(undefined); setReview(undefined)
      try { sessionStorage.removeItem(scope) } catch { setMessage('Order confirmed, but recovery storage could not be cleared. A reload may offer a safe replay of this same order.') }
      await state.refresh()
      state.unlockCheckout()
    } catch (error) {
      if (error instanceof CheckoutError && error.status === 401) {
        setRenew(true); setMessage('Sign in again, then retry this same checkout. Its original request is preserved.')
      } else if (error instanceof CheckoutError && ((error.status === 409 && conflicts.has(error.code)) || (error.status === 400 && error.code === 'invalid_input') || error.status === 403) && !existing) {
        // Only a first attempt's definitive rejection can release the lock. A
        // later rejection cannot establish the outcome of an earlier lost reply.
        try { sessionStorage.removeItem(scope) } catch { setMessage('Could not clear rejected attempt recovery data. Reload after restoring storage access.'); return }
        setPending(undefined); setReview(undefined)
        setMessage(error.status === 403 ? 'Security check rejected checkout (CSRF). Refresh before reviewing again.' : error.status === 400 ? 'Checkout request was invalid. Refresh and review again.' : `Checkout rejected (${error.code}). Refreshing cart; review again before placing an order.`)
        await state.refresh(); state.unlockCheckout()
      } else {
        setMessage(error instanceof CheckoutError && error.code === 'idempotency_conflict'
          ? 'Exceptional idempotency conflict. The original attempt is retained; do not create a replacement order. Contact support.'
          : error instanceof CheckoutError && error.status === 403
            ? 'Security check rejected this retry (CSRF). The earlier checkout outcome remains unresolved; its request is preserved.'
            : error instanceof CheckoutError && error.status === 400
              ? 'Invalid checkout request. The earlier outcome remains unresolved; its request is preserved for investigation.'
              : 'Checkout outcome is unresolved; the order may have been saved. Retry only this same checkout. Cart changes are blocked.')
      }
    } finally { guard.current = false; setBusy(false) }
  }
  return <section className="checkout" aria-labelledby="checkout-heading">
    <h2 id="checkout-heading" tabIndex={-1} ref={heading}>{order ? 'Simulated order confirmed' : pending ? 'Resolve simulated checkout' : 'Simulated checkout'}</h2>
    <p>No payment, tax, delivery, or real purchase occurs.</p>
    <div role="status" aria-live="polite"><p>{message}</p></div>
    {renew && <p><a href="/auth/logout">Sign in to recover checkout</a></p>}
    {pending ? <><p>The original key and exact request are preserved in this tab. Do not place another order to resolve this attempt.</p><button disabled={busy || !identityScope || scope !== identityScope} onClick={() => void submit(pending)}>Retry same checkout</button></> : order ? <>
      <p>Confirmation: <strong>{order.confirmation}</strong></p><p><time dateTime={order.created_at}>{new Date(order.created_at).toLocaleString()}</time></p>
      <ul>{order.items.map(i => <li key={i.product_id}>{i.name} · {i.package_label} · Quantity: {i.quantity} · Unit price: {usd.format(i.unit_price_cents / 100)} · Line total: {usd.format(i.line_total_cents / 100)}</li>)}</ul>
      <p>Total: {usd.format(order.total_cents / 100)} (simulated)</p>
      {state.blocked && <p>Order confirmed. Cart refresh failed; use Refresh cart to read the current cart.</p>}
      <button onClick={() => { setOrder(undefined); document.getElementById('product-search')?.focus() }}>Continue shopping</button>
    </> : currentReview ? <>
      <h3>Review simulated order</h3>
      <ul>{currentReview.items.map(i => <li key={i.product.id}>{i.product.name} · {i.product.package_label} · Quantity: {i.quantity} · Unit price: {usd.format(i.product.price_cents / 100)} · Line total: {usd.format(i.line_total_cents / 100)}{!i.product.available && ' · Unavailable'}</li>)}</ul>
      <p>Subtotal: {usd.format(currentReview.subtotal_cents / 100)}</p>
      {currentReview.items.some(i => !i.product.available) && <p>Unavailable items block confirmation. Update your cart and review again.</p>}
      <div className="cart-actions"><button className="secondary" disabled={busy} onClick={() => { setReview(undefined); document.getElementById('cart-heading')?.focus() }}>Back to cart</button>
      <button disabled={busy || state.busy || currentReview.items.some(i => !i.product.available)} onClick={() => void submit()}>Place simulated order</button></div>
    </> : <>{review && <p>Cart changed. Review the cart again before confirmation.</p>}<button disabled={busy || state.busy || state.blocked || !state.cart?.items.length || !identityScope || scope !== identityScope} onClick={() => void startReview()}>Review simulated order</button></>}
  </section>
}
