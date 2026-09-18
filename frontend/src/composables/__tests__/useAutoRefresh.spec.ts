import { describe, expect, it, beforeEach, vi } from 'vitest'
import { useAutoRefresh } from '@/composables/useAutoRefresh'

describe('useAutoRefresh', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.useFakeTimers()
  })

  it('starts ticking when enabled=true is restored from localStorage', () => {
    localStorage.setItem('test-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    let refreshes = 0
    const api = useAutoRefresh({ storageKey: 'test-auto-refresh', onRefresh: () => { refreshes++ } })

    expect(api.enabled.value).toBe(true)
    expect(api.intervalSeconds.value).toBe(5)
    expect(api.countdown.value).toBe(5)
    vi.advanceTimersByTime(5_000)
    expect(refreshes).toBe(1)
  })

  it('stays disabled when nothing is stored (default off)', () => {
    let refreshes = 0
    useAutoRefresh({ storageKey: 'test-auto-refresh-off', onRefresh: () => { refreshes++ } })
    vi.advanceTimersByTime(60_000)
    expect(refreshes).toBe(0)
  })

  it('skips ticks while a refresh is in flight', async () => {
    let resolveRefresh: () => void = () => {}
    const gate = new Promise<void>((r) => { resolveRefresh = r })
    const api = useAutoRefresh({ storageKey: 'test-auto-refresh-busy', onRefresh: () => gate })
    api.setEnabled(true)
    vi.advanceTimersByTime(30_000)
    expect(api.fetching.value).toBe(true)
    vi.advanceTimersByTime(10_000)
    resolveRefresh()
    await gate
    expect(api.fetching.value).toBe(false)
  })
})
