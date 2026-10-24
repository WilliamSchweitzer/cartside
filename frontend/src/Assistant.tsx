import { useEffect, useRef, useState } from 'react'
import type { Identity } from './App'
import type { CartState } from './useCart'
import { AssistantError, assistantRequest, boundedText, scenarios, utf8Bytes } from './assistantApi'
import type { AssistantResult, Suggestion } from './assistantApi'
import { usd } from './products'

type Choice = { state: 'offered' | 'pending' | 'accepted' | 'declined' | 'rejected' | 'uncertain'; message?: string }
type Exchange = { id: number; prompt: string; result?: AssistantResult; error?: string; choices: Choice[] }
export const historyLimit = 12
const protectedExchange = (e: Exchange) => !e.result && !e.error || e.choices.some(c => c.state === 'pending' || c.state === 'uncertain')

// A new verified identity gets a new memory-only transcript and async lifetime.
export default function Assistant({ state, identity }: { state: CartState; identity: Identity }) {
  if (identity.state !== 'ready') return null
  return <Panel key={JSON.stringify([identity.issuer, identity.subject])} state={state} />
}
function Panel({ state }: { state: CartState }) {
  const [draft, setDraft] = useState('')
  const [history, setHistory] = useState<Exchange[]>([])
  const [pending, setPending] = useState(false)
  const [status, setStatus] = useState('')
  const [cooldownUntil, setCooldownUntil] = useState(0)
  const [now, setNow] = useState(Date.now)
  const mounted = useRef(false)
  const sending = useRef(false)
  const sequence = useRef(0)
  const accepting = useRef(new Set<string>())
  const protectedIds = useRef(new Set<number>())
  const controller = useRef<AbortController | null>(null)
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false; controller.current?.abort() }
  }, [])
  useEffect(() => {
    if (!cooldownUntil) return
    const timer = window.setInterval(() => {
      const current = Date.now(); setNow(current)
      if (current >= cooldownUntil) window.clearInterval(timer)
    }, 250)
    return () => window.clearInterval(timer)
  }, [cooldownUntil])
  useEffect(() => {
    // Bound synchronous click guards along with their retained exchanges.
    const retained = new Set(history.flatMap(e => e.choices.map((_, i) => `${e.id}:${i}`)))
    for (const key of accepting.current) if (!retained.has(key)) accepting.current.delete(key)
    const ids = new Set(history.map(e => e.id))
    for (const id of protectedIds.current) if (!ids.has(id)) protectedIds.current.delete(id)
  }, [history])
  const cooldown = Math.max(0, Math.ceil((cooldownUntil - now) / 1000))
  const full = history.length >= historyLimit && history.every(protectedExchange)
  const valid = boundedText(draft, 256)
  async function send() {
    // Protect additions synchronously, including an Accept batched with Send.
    const protectedNow = (e: Exchange) => protectedExchange(e) || protectedIds.current.has(e.id)
    if (sending.current || Date.now() < cooldownUntil || !valid || (history.length >= historyLimit && history.every(protectedNow))) return
    const id = ++sequence.current
    const prompt = draft
    sending.current = true; setPending(true); setStatus('Asking the demo assistant…')
    setHistory(previous => {
      const next = [...previous]
      if (next.length >= historyLimit) next.splice(next.findIndex(e => !protectedNow(e)), 1)
      return [...next, { id, prompt, choices: [] }]
    })
    controller.current = new AbortController()
    const timer = window.setTimeout(() => controller.current?.abort(), 8000)
    try {
      const result = await assistantRequest(prompt, controller.current.signal)
      if (!mounted.current) return
      setHistory(previous => previous.map(e => e.id === id ? { ...e, result, choices: result.suggestions.map(() => ({ state: 'offered' })) } : e))
      setStatus(`Demo response received. ${result.suggestions.length} suggestions. Nothing was added to your cart.`)
      // Keep the draft for deliberate resends; history is never sent to Go.
    } catch (error) {
      if (!mounted.current) return
      const message = error instanceof AssistantError ? error.message : controller.current?.signal.aborted
        ? 'The assistant request timed out. You may deliberately send it again.'
        : 'The assistant request could not be completed. You may deliberately send it again.'
      if (error instanceof AssistantError && error.retrySeconds > 0) {
        const current = Date.now(); setNow(current); setCooldownUntil(current + error.retrySeconds * 1000)
      }
      setHistory(previous => previous.map(e => e.id === id ? { ...e, error: message } : e))
      setStatus(message)
    } finally {
      window.clearTimeout(timer)
      sending.current = false
      if (mounted.current) setPending(false)
    }
  }
  function choose(id: number, index: number, choice: Choice) {
    setHistory(previous => previous.map(e => e.id === id ? { ...e, choices: e.choices.map((c, i) => i === index ? choice : c) } : e))
  }
  async function accept(exchange: Exchange, suggestion: Suggestion, index: number) {
    const key = `${exchange.id}:${index}`
    const choice = exchange.choices[index]
    if (accepting.current.has(key) || !['offered', 'rejected'].includes(choice.state) || state.busy || state.blocked || !suggestion.available) return
    accepting.current.add(key)
    protectedIds.current.add(exchange.id)
    choose(exchange.id, index, { state: 'pending' }); setStatus('Adding the selected suggestion…')
    const result = await state.mutate('POST', suggestion.product_id, suggestion.name, suggestion.quantity)
    if (!mounted.current) return
    if (result.outcome !== 'uncertain') protectedIds.current.delete(exchange.id)
    if (result.outcome === 'confirmed') {
      choose(exchange.id, index, { state: 'accepted', message: result.refreshed ? 'Accepted. Added to your cart.' : 'Accepted. Change saved; cart refresh failed. Use Refresh cart.' })
      setStatus('Suggestion accepted. Cart addition confirmed.')
    } else if (result.outcome === 'uncertain') {
      choose(exchange.id, index, { state: 'uncertain', message: 'Outcome uncertain. Inspect and manually adjust your cart. Do not accept this suggestion again.' })
      setStatus('Addition outcome uncertain. Inspect and manually adjust your cart.')
    } else {
      accepting.current.delete(key)
      const message = result.outcome === 'rejected' ? result.message : 'Addition did not start. Wait until cart and checkout controls are ready.'
      choose(exchange.id, index, { state: 'rejected', message }); setStatus(message)
    }
  }
  return <section className="assistant" aria-labelledby="assistant-heading">
    <h2 id="assistant-heading">Demo shopping assistant</h2>
    <p>Deterministic simulations, not a live language model. Each message is handled independently; earlier messages are not sent or remembered by the server.</p>
    <p>Dietary and allergen suitability is unverified. Check product labels yourself.</p>
    <div className="cart-actions" role="group" aria-label="Example prompts">{scenarios.map(prompt => <button type="button" className="secondary" key={prompt} disabled={pending} onClick={() => setDraft(prompt)}>{prompt}</button>)}</div>
    <form onSubmit={event => { event.preventDefault(); void send() }}>
      <label htmlFor="assistant-message">Message to demo assistant</label>
      <textarea id="assistant-message" value={draft} readOnly={pending} onChange={event => setDraft(event.target.value)} aria-describedby="assistant-help" aria-invalid={draft.length > 0 && !valid} />
      <p id="assistant-help">{utf8Bytes(draft)} / 256 UTF-8 bytes. Nonblank text only; characters may use multiple bytes.</p>
      <button type="submit" disabled={pending || cooldown > 0 || !valid || full}>Send message</button>
    </form>
    <p role="status" aria-live="polite" aria-atomic="true">{status}</p>
    {cooldown > 0 && <p>Try again in {cooldown} seconds.</p>}
    {full && <p>History is full of unresolved additions. Inspect and manually adjust your cart; new messages are paused to preserve these outcomes.</p>}
    <p>Only the latest {historyLimit} exchanges are kept in this panel. Pending and uncertain additions are retained. Reloading or changing accounts clears this history.</p>
    <ol className="assistant-history" aria-label="Assistant messages">{history.map(exchange => <li key={exchange.id}>
      <p><strong>You:</strong> {exchange.prompt}</p>
      {exchange.error ? <p>{exchange.error}</p> : exchange.result ? <>
        <p><strong>{exchange.result.provider}</strong></p><p>{exchange.result.text}</p>
        <ul className="assistant-suggestions">{exchange.result.suggestions.map((s, index) => {
          const choice = exchange.choices[index]
          const actionable = choice.state === 'offered' || choice.state === 'rejected'
          return <li key={s.product_id}>
            <h3>{s.name}</h3><p>{s.package_label} · Unit price: {usd.format(s.price_cents / 100)} · Quantity: {s.quantity}</p><p>{s.reason}</p>
            {!s.available && <p>Currently unavailable.</p>}
            <div className="cart-actions"><button type="button" disabled={!actionable || state.busy || state.blocked || !s.available} onClick={() => void accept(exchange, s, index)} aria-label={`Accept ${s.name}`}>Accept</button>
              <button type="button" className="secondary" disabled={!actionable} onClick={() => {
                if (accepting.current.has(`${exchange.id}:${index}`)) return
                accepting.current.add(`${exchange.id}:${index}`)
                choose(exchange.id, index, { state: 'declined', message: 'Declined. Nothing was added.' }); setStatus('Suggestion declined. Cart unchanged.')
              }} aria-label={`Decline ${s.name}`}>Decline</button></div>
            {choice.state === 'pending' && <p>Adding suggestion…</p>}
            {choice.message && <p>{choice.message}</p>}
          </li>
        })}</ul>
      </> : <p>Waiting for the demo response…</p>}
    </li>)}</ol>
  </section>
}
