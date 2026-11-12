import { strict as assert } from 'node:assert'
import { test } from 'node:test'
import { mergeEvents, type LogEvent } from './types.ts'

const event = (sequence: number, message = 'hello', epoch = 'process-a'): LogEvent => ({ id: `${epoch}:${sequence}`, sourceId: 'source', host: 'api', path: '/api.log', receivedAt: new Date(1700000000000 + sequence).toISOString(), level: 'info', message })

test('replaying SSE batches does not duplicate events, and sequence ordering is numeric', () => {
  const result = mergeEvents([event(9), event(10)], [event(10), event(11), event(2)])
  assert.deepEqual(result.map(item => item.id), ['process-a:2', 'process-a:9', 'process-a:10', 'process-a:11'])
})
test('the browser retains the latest 3000 events', () => {
  const result = mergeEvents([], Array.from({ length: 4000 }, (_, i) => event(i)))
  assert.equal(result.length, 3000)
  assert.equal(result[0].id, 'process-a:1000')
  assert.equal(result.at(-1)?.id, 'process-a:3999')
})
test('large lines hit the memory budget before the event count limit', () => {
  const result = mergeEvents([], Array.from({ length: 500 }, (_, i) => event(i, 'x'.repeat(32768))))
  assert.ok(result.length > 0 && result.length < 100)
  assert.equal(result.at(-1)?.id, 'process-a:499')
})
test('a backend restart does not collide with earlier event IDs', () => {
  const result = mergeEvents([event(1)], [event(1, 'after restart', 'process-b')])
  assert.equal(result.length, 2)
  assert.equal(result[1].message, 'after restart')
})
