export type Host = { id: string; name: string; address: string; port: number; username: string; fingerprint: string }
export type ParserMode = 'regular' | 'tx'
export type Source = { id: string; hostId: string; path: string; parser?: ParserMode }
export type ServerGroup = { id: string; name: string; hostIds: string[] }
export type SourceStatus = { state: string; detail: string; updatedAt: string }
export type Config = { hosts: Host[]; sources: Source[]; groups: ServerGroup[]; statuses: Record<string, SourceStatus>; demo: boolean; buffer: { count: number; bytes: number; maxBytes: number } }
export type LogEvent = { id: string; sourceId: string; host: string; path: string; receivedAt: string; timestamp?: string; level: string; message: string; fields?: Record<string, unknown> }
export type Credentials = { method: string; password?: string; privateKey?: string; passphrase?: string; reason?: string }
export type EditResult = { host: Host; reconnectRequired: boolean }
export async function api<T>(path: string, method = 'GET', body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`/api${path}`, { method, headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), signal })
  const data = await response.json()
  if (!response.ok) throw new Error(data.error || `Request failed (${response.status})`)
  return data as T
}

export function mergeEvents(previous: LogEvent[], incoming: LogEvent[]): LogEvent[] {
  const map = new Map(previous.map(event => [event.id, event]))
  incoming.forEach(event => map.set(event.id, event))
  const result = Array.from(map.values()).sort((a, b) => {
    const [ae, as] = a.id.split(':'); const [be, bs] = b.id.split(':')
    return ae === be ? Number(as) - Number(bs) : Date.parse(a.receivedAt) - Date.parse(b.receivedAt)
  })
  let bytes = 0; let start = result.length
  while (start > 0 && result.length - start < 3000) {
    const event = result[start - 1]
    bytes += event.message.length * 6 + 1024
    if (bytes > 8 * 1024 * 1024) break
    start--
  }
  return result.slice(start)
}
