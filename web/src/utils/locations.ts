import type { Location } from '@/api/locations'

export function locationLabel(l: Pick<Location, 'name' | 'display_name' | 'location_code'>): string {
  const name = l.display_name?.trim() || l.name
  return l.location_code ? `${name} (${l.location_code})` : name
}
