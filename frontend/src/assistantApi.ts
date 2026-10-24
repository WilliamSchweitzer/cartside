import { cents } from './products'

export const scenarios = ['breakfast for two', 'vegetarian dinner', 'snacks for a picnic']
export const utf8Bytes = (text: string) => new TextEncoder().encode(text).length
// TextEncoder replaces lone surrogates. Reject them to match Go's JSON contract.
export function boundedText(value: unknown, bytes: number): value is string {
  return typeof value === 'string' && /[^\p{White_Space}]/u.test(value) &&
    !/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value) && utf8Bytes(value) <= bytes
}
export type Suggestion = { product_id: string; name: string; package_label: string; price_cents: number; currency: 'USD'; available: boolean; quantity: number; reason: string }
export type AssistantResult = { version: 1; provider: 'simulated: cartside-mock-v1'; text: string; suggestions: Suggestion[] }
export function validAssistantResult(value: unknown): value is AssistantResult {
  if (!value || typeof value !== 'object') return false
  const r = value as AssistantResult
  if (r.version !== 1 || r.provider !== 'simulated: cartside-mock-v1' || !boundedText(r.text, 400) || !Array.isArray(r.suggestions) || r.suggestions.length > 5) return false
  const ids = new Set<string>()
  return r.suggestions.every(s => {
    if (!s || typeof s.product_id !== 'string' || !/^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/.test(s.product_id) || ids.has(s.product_id) ||
      !boundedText(s.name, 160) || !boundedText(s.package_label, 100) || !boundedText(s.reason, 120) || !cents(s.price_cents) || s.currency !== 'USD' ||
      typeof s.available !== 'boolean' || !Number.isInteger(s.quantity) || s.quantity < 1 || s.quantity > 99) return false
    ids.add(s.product_id)
    return true
  })
}
export class AssistantError extends Error {
  constructor(public code: string, message: string, public retrySeconds = 0) { super(message) }
}
const errors: Record<string, string> = {
  assistant_unavailable: 'The demo assistant is disabled. You can still shop manually.',
  invalid_input: 'Use a nonblank message of at most 256 UTF-8 bytes.',
  payload_too_large: 'The message is too large.',
  assistant_rate_limited: 'Your demo assistant request limit was reached.',
  assistant_busy: 'An assistant request is already running for your account.',
  assistant_capacity: 'The demo assistant is at capacity.',
  provider_rate_limited: 'The simulated provider request limit was reached.',
  timeout: 'The assistant timed out. You may deliberately send the message again.',
  catalog_changed: 'Suggested products changed. Send the message again for fresh suggestions.',
  catalog_unavailable: 'The catalog could not be read for suggestions.',
  invalid_provider_output: 'The assistant returned an invalid response. No suggestions were used.',
  provider_unavailable: 'The assistant provider is unavailable. You can still shop manually.',
}
export function retrySeconds(header: string | null, now = Date.now()) {
  if (!header) return 0
  if (/^\d+$/.test(header)) { const n = Number(header); return Number.isSafeInteger(n) ? n : 0 }
  const date = Date.parse(header)
  return Number.isFinite(date) ? Math.max(0, Math.ceil((date - now) / 1000)) : 0
}
export async function assistantRequest(message: string, signal: AbortSignal): Promise<AssistantResult> {
  if (!boundedText(message, 256)) throw new AssistantError('invalid_input', errors.invalid_input)
  const response = await fetch('/api/assistant/messages', {
    method: 'POST', credentials: 'same-origin', cache: 'no-store', signal,
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Protection': '1' }, body: JSON.stringify({ message }),
  })
  if (!response.ok) {
    const body: unknown = await response.json().catch(() => null)
    const code = body && typeof body === 'object' && 'code' in body && typeof body.code === 'string' ? body.code : ''
    const message = response.status === 401 ? 'Your session needs renewal. Return to sign-in to use the assistant.'
      : response.status === 403 ? 'The assistant security check failed (CSRF). Refresh the page before trying again.'
        : (Object.hasOwn(errors, code) ? errors[code] : undefined) ?? (response.status === 429 ? 'The assistant request limit was reached.' : 'The demo assistant is unavailable. You can still shop manually.')
    throw new AssistantError(code, message, retrySeconds(response.headers.get('Retry-After')))
  }
  const result: unknown = await response.json().catch(() => null)
  if (!validAssistantResult(result)) throw new AssistantError('invalid_provider_output', errors.invalid_provider_output)
  return result
}
