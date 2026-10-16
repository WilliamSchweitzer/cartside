import { useEffect, useState } from 'react'
import Catalog from './Catalog'
import Cart from './Cart'
import Checkout from './Checkout'
import { useCart } from './useCart'

export type Identity = { state: 'loading' | 'expired' | 'failed' } | { state: 'ready'; subject: string; issuer: string }

function SignedInIdentity({ identity, setIdentity }: { identity: Identity; setIdentity: (value: Identity) => void }) {
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    const timeout = window.setTimeout(() => controller.abort(), 8000)
    let active = true
    async function load() {
      try {
        const response = await fetch('/api/me', { signal: controller.signal, cache: 'no-store' })
        if (response.status === 401) {
          if (active) setIdentity({ state: 'expired' })
          return
        }
        if (!response.ok) throw new Error('Identity service unavailable')
        const body: unknown = await response.json()
        if (typeof body !== 'object' || body === null || !('subject' in body) ||
          typeof body.subject !== 'string' || !body.subject || !('issuer' in body) ||
          typeof body.issuer !== 'string' || !body.issuer) throw new Error('Invalid identity response')
        if (active) setIdentity({ state: 'ready', subject: body.subject, issuer: body.issuer })
      } catch {
        if (active) setIdentity({ state: 'failed' })
      } finally {
        window.clearTimeout(timeout)
      }
    }
    void load()
    return () => { active = false; controller.abort(); window.clearTimeout(timeout) }
  }, [attempt, setIdentity])
  return <section className="identity" aria-label="Signed-in identity">
    <div role="status" aria-live="polite">
      {identity.state === 'loading' && <p>Loading identity…</p>}
      {identity.state === 'ready' && <p title={`Issuer: ${identity.issuer}`}>Signed in · <span title={identity.subject}>{identity.subject}</span></p>}
      {identity.state === 'expired' && <p>Your session has expired or needs renewal. <a href="/auth/logout">Return to sign-in</a></p>}
      {identity.state === 'failed' && <p>The identity service is unavailable. Please try again.</p>}
    </div>
    {identity.state === 'failed' && <button type="button" onClick={() => {
      setIdentity({ state: 'loading' }); setAttempt(value => value + 1)
    }}>Retry identity check</button>}
  </section>
}

type Readiness = 'loading' | 'ready' | 'unavailable' | 'network-error'

const messages: Record<Readiness, { title: string; detail: string }> = {
  loading: { title: 'Checking backend…', detail: 'Waiting for a readiness response.' },
  ready: { title: 'Backend ready', detail: 'The backend can reach PostgreSQL.' },
  unavailable: { title: 'Backend unavailable', detail: 'The readiness check did not succeed. Please try again.' },
  'network-error': { title: 'Network error', detail: 'Could not complete the readiness request. Check your connection and retry.' },
}

export default function App() {
  const cart = useCart(true)
  const [identity, setIdentity] = useState<Identity>({ state: 'loading' })
  const [readiness, setReadiness] = useState<Readiness>('loading')
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    const timeout = window.setTimeout(() => controller.abort(), 8000)
    let active = true

    async function check() {
      try {
        const response = await fetch('/api/health/ready', {
          signal: controller.signal,
          cache: 'no-store',
        })
        // HTTP errors (including gateway failures) mean unavailable, not a network error.
        if (!response.ok) {
          if (active) setReadiness('unavailable')
          return
        }
        const body: unknown = await response.json()
        const ready = typeof body === 'object' && body !== null &&
          'status' in body && body.status === 'ok'
        if (active) setReadiness(ready ? 'ready' : 'unavailable')
      } catch {
        if (active) setReadiness('network-error')
      } finally {
        window.clearTimeout(timeout)
      }
    }
    void check()
    return () => {
      active = false
      controller.abort()
      window.clearTimeout(timeout)
    }
  }, [attempt])

  return (
    <main>
      <header className="site-header">
        <a className="brand" href="/">Cartside<span>Groceries for your everyday</span></a>
        <div className="account"><SignedInIdentity identity={identity} setIdentity={setIdentity} /><a href="/auth/logout">Sign out</a></div>
      </header>
      <div className="welcome">
        <p className="eyebrow">Your neighborhood essentials</p>
        <h1>A fresh look at<br />everyday groceries.</h1>
        <p>Browse the shelves and find your favorites.</p>
        <p className="browse-note">Build your grocery cart below.</p>
      </div>
      <a href="#cart">Go to your cart</a>
      <Catalog cart={cart} />
      <Cart state={cart} />
      <Checkout state={cart} identity={identity} />
      <details className="diagnostics">
        <summary>Service diagnostics</summary>
      <section aria-labelledby="readiness-heading">
        <h2 id="readiness-heading">Backend readiness</h2>
        <div role="status" aria-live="polite" aria-atomic="true" className={`status ${readiness}`}>
          <strong>{messages[readiness].title}</strong>
          <p>{messages[readiness].detail}</p>
        </div>
        <button type="button" disabled={readiness === 'loading'} onClick={() => {
          setReadiness('loading')
          setAttempt((value) => value + 1)
        }}>Retry readiness check</button>
      </section>
      </details>
      <footer>Cartside · Everyday starts here.</footer>
    </main>
  )
}
