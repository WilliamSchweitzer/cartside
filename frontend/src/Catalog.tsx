import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'

type Product = {
  id: string; name: string; package_label: string; image_reference: string
  price_cents: number; currency: 'USD'; category: string; available: boolean
}
type Page = { items: Product[]; limit: number; offset: number; has_more: boolean }
type Result = { state: 'loading' | 'failed' | 'expired' } | { state: 'ready'; page: Page }
const usd = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' })
const fallback = '/images/products/fallback.svg'

function validPage(value: unknown, offset: number): value is Page {
  if (typeof value !== 'object' || value === null) return false
  const page = value as Page
  return page.limit === 20 && page.offset === offset && typeof page.has_more === 'boolean' &&
    Array.isArray(page.items) && page.items.length <= 20 && page.items.every(p =>
      p && typeof p.id === 'string' && typeof p.name === 'string' &&
      typeof p.package_label === 'string' && typeof p.image_reference === 'string' &&
      typeof p.category === 'string' && typeof p.available === 'boolean' &&
      p.currency === 'USD' && Number.isInteger(p.price_cents) && p.price_cents >= 0)
}

function ProductCard({ product }: { product: Product }) {
  // Only local product assets are loaded, including for developer-edited records.
  const source = /^\/images\/products\/[a-zA-Z0-9_-]+\.(svg|png|webp|jpg)$/.test(product.image_reference)
    ? product.image_reference : fallback
  const [brokenSource, setBrokenSource] = useState<string | null>(null)
  return <li className="product-card">
    <div className="product-image"><img src={brokenSource === source ? fallback : source}
      alt="" loading="lazy" onError={() => setBrokenSource(source)} /></div>
    <div className="product-info">
      <p className="category">{product.category}</p>
      <h3>{product.name}</h3>
      <p className="package">{product.package_label}</p>
      <p className="price">{usd.format(product.price_cents / 100)}</p>
      <p className={`availability ${product.available ? '' : 'unavailable'}`}>
        {product.available ? 'Available' : 'Currently unavailable'}
      </p>
    </div>
  </li>
}

export default function Catalog() {
  const [draft, setDraft] = useState('')
  const [request, setRequest] = useState({ query: '', offset: 0, attempt: 0 })
  const [result, setResult] = useState<Result>({ state: 'loading' })
  const [validation, setValidation] = useState('')
  useEffect(() => {
    const controller = new AbortController()
    let active = true
    const timeout = window.setTimeout(() => controller.abort(), 8000)
    async function load() {
      try {
        const params = new URLSearchParams({ q: request.query, limit: '20', offset: String(request.offset) })
        const response = await fetch(`/api/products?${params}`, {
          signal: controller.signal, cache: 'no-store', credentials: 'same-origin',
        })
        if (response.status === 401) {
          if (active) setResult({ state: 'expired' })
          return
        }
        if (!response.ok) throw new Error('Catalog unavailable')
        const page: unknown = await response.json()
        if (!validPage(page, request.offset)) throw new Error('Invalid catalog response')
        if (active) setResult({ state: 'ready', page })
      } catch {
        if (active) setResult({ state: 'failed' })
      } finally { window.clearTimeout(timeout) }
    }
    void load()
    return () => { active = false; controller.abort(); window.clearTimeout(timeout) }
  }, [request])

  function browse(query: string, offset: number) {
    setResult({ state: 'loading' })
    setRequest(previous => ({ query, offset, attempt: previous.attempt + 1 }))
  }
  function submit(event: FormEvent) {
    event.preventDefault()
    const query = draft.trim()
    if ([...query].length > 200 || query.includes('\0')) {
      setValidation('Use at most 200 characters and no null characters.')
      return
    }
    setValidation('')
    browse(query, query === request.query ? request.offset : 0)
  }

  return <section className="catalog" aria-labelledby="catalog-heading">
    <form className="search" onSubmit={submit} role="search">
      <label htmlFor="product-search">Search groceries</label>
      <div className="search-controls">
        <input id="product-search" type="search" value={draft} placeholder="Try apples, bread, or milk"
          aria-describedby={validation ? 'search-error' : undefined}
          aria-invalid={Boolean(validation)} onChange={event => setDraft(event.target.value)} />
        <button type="submit">Search</button>
        <button type="button" className="secondary" onClick={() => {
          setDraft(''); setValidation(''); browse('', 0)
        }}>Clear</button>
      </div>
      {validation && <p id="search-error" role="alert">{validation}</p>}
    </form>
    <div className="catalog-heading">
      <h2 id="catalog-heading">{request.query ? `Results for “${request.query}”` : 'Explore the catalog'}</h2>
      <p>Everyday groceries, simply presented.</p>
    </div>
    <div role="status" aria-live="polite" aria-atomic="true">
      {result.state === 'loading' && <p className="catalog-message">Loading groceries…</p>}
      {result.state === 'failed' && <p className="catalog-message">We couldn’t load the groceries. The service may be unavailable, or your connection may have timed out. Please retry.</p>}
      {result.state === 'expired' && <p className="catalog-message">Your session has expired or needs renewal. <a href="/auth/logout">Return to sign-in</a></p>}
      {result.state === 'ready' && <p className="results-note">{result.page.items.length === 0
        ? 'No groceries found. Try another search or clear it to browse the catalog.'
        : `Showing products ${request.offset + 1}–${request.offset + result.page.items.length}.`}</p>}
    </div>
    {result.state === 'failed' && <button onClick={() => browse(request.query, request.offset)}>Retry catalog</button>}
    {result.state === 'ready' && <>
      <ul className="product-grid">{result.page.items.map(product => <ProductCard key={product.id} product={product} />)}</ul>
      <nav className="pagination" aria-label="Catalog pages">
        <button className="secondary" disabled={request.offset === 0} onClick={() => browse(request.query, Math.max(0, request.offset - 20))}>Previous</button>
        <span>Page {request.offset / 20 + 1}</span>
        <button className="secondary" disabled={!result.page.has_more} onClick={() => browse(request.query, request.offset + 20)}>Next</button>
      </nav>
    </>}
  </section>
}
