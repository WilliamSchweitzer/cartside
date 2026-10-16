import { useCallback, useEffect, useRef, useState } from 'react'
import { cartRequest, CartError, cartErrorMessage } from './cartApi'
import type { Cart } from './cartApi'

export function useCart() {
  const [cart, setCart] = useState<Cart>()
  const [busy, setBusy] = useState(true)
  const [blocked, setBlocked] = useState(true)
  const [expired, setExpired] = useState(false)
  const [message, setMessage] = useState('Loading cart…')
  const guard = useRef(false)
  const generation = useRef(0)
  const mounted = useRef(false)
  const ready = useRef(false)

  const read = useCallback(async (prefix = '') => {
    const ticket = ++generation.current
    try {
      const result = await cartRequest()
      if (!mounted.current || ticket !== generation.current) return
      setCart(result)
      ready.current = true
      setBlocked(false)
      setExpired(false)
      setMessage(prefix || 'Cart refreshed.')
      return true
    } catch (error) {
      if (!mounted.current || ticket !== generation.current) return
      ready.current = false
      setBlocked(true)
      setExpired(error instanceof CartError && error.status === 401)
      setMessage(`${prefix ? `${prefix} ` : ''}Cart refresh failed. ${cartErrorMessage(error)}`)
      return false
    }
  }, [])

  useEffect(() => {
    mounted.current = true
    let active = true
    void Promise.resolve().then(async () => {
      if (!active) return
      const result = await read()
      if (active && result !== undefined) setBusy(false)
    })
    return () => { active = false; mounted.current = false; ready.current = false }
  }, [read])

  async function refresh() {
    if (guard.current) return
    guard.current = true
    setBusy(true)
    setMessage('Loading cart…')
    try { await read() } finally { guard.current = false; if (mounted.current) setBusy(false) }
  }
  async function mutate(method: 'POST' | 'PUT' | 'DELETE', id: string, name: string, quantity?: number) {
    if (guard.current || !ready.current) return
    guard.current = true
    ++generation.current // Any read begun before this change is obsolete.
    setBusy(true)
    setMessage('Saving cart change…')
    try {
      await cartRequest(method, id, quantity)
      if (!mounted.current) return
      const success = method === 'POST' ? `Added one ${name} to cart.` : method === 'DELETE' ? `Removed ${name} from cart.` : `Updated quantity for ${name}.`
      const refreshed = await read(success)
      if (!refreshed && mounted.current) setMessage(previous => `Change saved, cart refresh failed. ${previous}`)
    } catch (error) {
      if (!mounted.current) return
      if (!(error instanceof CartError) || error.status >= 500) {
        ready.current = false
        setBlocked(true)
        const uncertain = `${cartErrorMessage(error)} The change outcome is uncertain; it may have been saved. Do not repeat the addition blindly.`
        setMessage(`${uncertain} Refreshing cart…`)
        await read(`${uncertain} Review the refreshed cart before making another change.`)
      } else {
        const sessionExpired = error.status === 401
        if (sessionExpired) { ready.current = false; setBlocked(true); setExpired(true) }
        setMessage(`Change failed. ${cartErrorMessage(error)}`)
      }
    } finally { guard.current = false; if (mounted.current) setBusy(false) }
  }
  return { cart, busy, blocked, expired, message, refresh, mutate }
}
export type CartState = ReturnType<typeof useCart>
