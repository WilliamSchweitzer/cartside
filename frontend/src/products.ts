export type Product = {
  id: string; name: string; package_label: string; image_reference: string
  price_cents: number; currency: 'USD'; category: string; available: boolean
}
export const usd = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' })
export const cents = (n: unknown): n is number => Number.isSafeInteger(n) && (n as number) >= 0
export function validProduct(value: unknown): value is Product {
  if (!value || typeof value !== 'object') return false
  const p = value as Product
  return typeof p.id === 'string' && /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(p.id) &&
    typeof p.name === 'string' && typeof p.package_label === 'string' &&
    typeof p.image_reference === 'string' && typeof p.category === 'string' &&
    typeof p.available === 'boolean' && p.currency === 'USD' && cents(p.price_cents)
}
