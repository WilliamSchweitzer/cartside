import { useState } from 'react'
import type { FormEvent } from 'react'
import type { CartItem } from './cartApi'
import type { CartState } from './useCart'
import { usd } from './products'

function Item({ item, state }: { item: CartItem; state: CartState }) {
  const { product: p, quantity } = item
  const [draft, setDraft] = useState(String(quantity))
  const [previousQuantity, setPreviousQuantity] = useState(quantity)
  if (quantity !== previousQuantity) {
    setPreviousQuantity(quantity)
    setDraft(String(quantity))
  }
  const value = Number(draft)
  const valid = /^\d+$/.test(draft) && Number.isInteger(value) && value >= 1 && value <= 99 && (p.available || value <= quantity)
  const disabled = state.busy || state.blocked
  function update(event: FormEvent) {
    event.preventDefault()
    if (valid) void state.mutate('PUT', p.id, p.name, value)
  }
  return <li className="cart-item">
    <div><h3>{p.name}</h3><p>{p.package_label}</p>
      <p>Current unit price: {usd.format(p.price_cents / 100)}</p>
      <p>{p.available ? 'Available' : 'Currently unavailable — you may reduce the quantity or remove this item.'}</p>
      <p>Quantity: {quantity} · Line total: <strong>{usd.format(item.line_total_cents / 100)}</strong></p></div>
    <form onSubmit={update} className="quantity-form">
      <label htmlFor={`quantity-${p.id}`}>Quantity for {p.name}</label>
      <input id={`quantity-${p.id}`} type="number" min="1" max={p.available ? 99 : quantity} step="1" required
        value={draft} onChange={event => setDraft(event.target.value)} aria-describedby={`quantity-help-${p.id}`}
        aria-invalid={!valid} readOnly={disabled} />
      <small id={`quantity-help-${p.id}`}>Whole units, 1–{p.available ? 99 : quantity}.</small>
      <div className="cart-actions"><button disabled={disabled || !valid || value === quantity} aria-label={`Update quantity for ${p.name}`}>Update</button>
        <button className="secondary" type="button" disabled={disabled} aria-label={`Remove ${p.name}`} onClick={() => void state.mutate('DELETE', p.id, p.name)}>Remove</button></div>
    </form>
  </li>
}
export default function Cart({ state }: { state: CartState }) {
  return <section className="cart" aria-labelledby="cart-heading" id="cart">
    <h2 id="cart-heading" tabIndex={-1}>Your cart</h2>
    <div role="status" aria-live="polite" aria-atomic="true"><p>{state.message}</p></div>
    {state.expired && <p><a href="/auth/logout">Return to sign-in</a></p>}
    <button className="secondary" disabled={state.busy} onClick={() => void state.refresh()}>Refresh cart</button>
    {state.cart && <>
      {state.blocked && <p>Cart details may be out of date. Changes are disabled until a successful refresh.</p>}
      {state.cart.items.length === 0 ? <p>Your cart is empty. Add groceries from the catalog.</p> : <>
        <p>{state.cart.items.reduce((sum, item) => sum + item.quantity, 0)} total units · {state.cart.items.length} distinct items</p>
        <ul className="cart-items">{state.cart.items.map(item => <Item key={item.product.id} item={item} state={state} />)}</ul>
        <p className="cart-subtotal">Subtotal: {usd.format(state.cart.subtotal_cents / 100)}</p>
      </>}
    </>}
    <p>Subtotal includes unavailable items. Prices are confirmed again at checkout.</p>
  </section>
}
