import { describe, expect, it, vi } from 'vitest'
import { createDisplayInput } from './display-input'

function setup() {
  const trace: unknown[] = []
  const failed = vi.fn()
  const input = createDisplayInput(value => trace.push(value), failed)
  let resolve!: (ok: boolean) => void
  const sync = new Promise<boolean>(yes => { resolve = yes })
  const down = { type: 'pointer', x: 1, y: 1, button_mask: 1 }
  const up = { ...down, button_mask: 0 }
  return { trace, failed, input, sync, resolve, down, up }
}

describe('display input clipboard barrier', () => {
  it('sends clipboard text before the entire menu click, preserving later keys', async () => {
    const { input, sync, resolve, trace, down, up } = setup()
    input.waitForClipboard(sync)
    input.input(down)
    input.input(up)
    input.input({ type: 'key', keysym: 65, down: true })
    expect(trace).toEqual([])
    trace.push('clipboard_sync')
    resolve(true)
    await sync
    expect(trace).toEqual(['clipboard_sync', down, up, { type: 'key', keysym: 65, down: true }])
  })

  it('coalesces drag movement without dropping the release', async () => {
    const { input, sync, resolve, trace, down, up } = setup()
    input.waitForClipboard(sync)
    input.input(down)
    for (let x = 2; x < 1000; x++) input.input({ ...down, x })
    input.input(up)
    resolve(true)
    await sync
    expect(trace).toEqual([down, { ...down, x: 999 }, up])
  })

  it('drops a failed click, releases previously held keys, and allows retry', async () => {
    const { input, sync, resolve, trace, failed, down, up } = setup()
    input.input({ type: 'key', keysym: 1, down: true })
    input.waitForClipboard(sync)
    input.input(down)
    input.input(up)
    resolve(false)
    await sync
    expect(failed).toHaveBeenCalledOnce()
    expect(trace).toEqual([{ type: 'key', keysym: 1, down: true }, { type: 'key', keysym: 1, down: false }])
    input.input(down)
    input.input(up)
    expect(trace.slice(-2)).toEqual([down, up])
  })

  it('does not turn movement into a mouse down after a failure mid-drag', async () => {
    const { input, sync, resolve, trace, down, up } = setup()
    input.waitForClipboard(sync)
    input.input(down)
    resolve(false)
    await sync
    input.input({ ...down, x: 2 })
    expect(trace).toEqual([])
    input.input(up)
    input.input(down)
    expect(trace).toEqual([up, down])
  })

  it('discards old queued input after reconnect, hide, or native paste', async () => {
    const { input, sync, resolve, trace, down, up } = setup()
    input.waitForClipboard(sync)
    input.input(down)
    input.input(up)
    input.cancel()
    resolve(true)
    await sync
    expect(trace).toEqual([])
  })
})
