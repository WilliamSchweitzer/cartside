import { useEffect, useState } from 'react'

type Readiness = 'loading' | 'ready' | 'unavailable' | 'network-error'

const messages: Record<Readiness, { title: string; detail: string }> = {
  loading: { title: 'Checking backend…', detail: 'Waiting for a readiness response.' },
  ready: { title: 'Backend ready', detail: 'The backend can reach PostgreSQL.' },
  unavailable: { title: 'Backend unavailable', detail: 'The readiness check did not succeed. Please try again.' },
  'network-error': { title: 'Network error', detail: 'Could not complete the readiness request. Check your connection and retry.' },
}

export default function App() {
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
      <p className="eyebrow">A grocery storefront in the making</p>
      <h1>Cartside</h1>
      <p className="intro">A foundation for a simpler grocery shopping experience.</p>
      <p>This first screen checks the connection to the backend. Shopping features are still in development.</p>
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
    </main>
  )
}
