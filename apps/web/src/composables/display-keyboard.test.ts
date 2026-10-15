import { describe, expect, it, vi } from 'vitest'
import { createDisplayKeyboard } from './display-keyboard'
import { createDisplayInput } from './display-input'

function key(key: string, modifiers: Partial<KeyboardEvent> = {}) {
  return { key, ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, repeat: false, preventDefault: vi.fn(), ...modifiers }
}

function setup(mac = true) {
  const held = new Set<number>()
  const presses: { keysym: number; modifiers: number[] }[] = []
  const copy = vi.fn()
  const pasteShortcut = vi.fn()
  let active = true
  let text = 'prefix suffix'
  let cursor = 7
  let anchor = cursor
  const send = vi.fn((event: { keysym: number; down: boolean }) => {
    if (!event.down) { held.delete(event.keysym); return }
    held.add(event.keysym)
    presses.push({ keysym: event.keysym, modifiers: [...held].filter(key => key >= 0xffe1) })
    if (event.keysym === 0xff50) {
      cursor = 0
      if (!held.has(0xffe1)) anchor = cursor
    } else if (event.keysym === 97 && held.has(0xffe3)) {
      anchor = 0
      cursor = text.length
    } else if (event.keysym === 0xff08) {
      const start = Math.min(cursor, anchor)
      const end = Math.max(cursor, anchor)
      const from = start !== end ? start : held.has(0xffe3)
        ? (text.slice(0, start).match(/^(.*?)(?:\S+\s*)$/)?.[1]?.length ?? 0)
        : Math.max(0, start - 1)
      text = text.slice(0, from) + text.slice(end)
      cursor = anchor = from
    }
  })
  const keyboard = createDisplayKeyboard({ mac, active: () => active, send, copy, pasteShortcut })
  return { keyboard, held, presses, send, copy, pasteShortcut, text: () => text, deactivate: () => { active = false } }
}

describe('remote desktop editing keys', () => {
  it('deletes to the beginning of the line with Command+Backspace', () => {
    const { keyboard, text, held, send } = setup()
    keyboard.keydown(key('Meta', { metaKey: true }))
    keyboard.keydown(key('Backspace', { metaKey: true }))
    expect(text()).toBe('suffix')
    expect([...held]).toEqual([])
    expect(send.mock.calls.some(([event]) => event.keysym === 0xffeb && event.down)).toBe(false)
  })

  it('selects all with Command+A and deletes the selection', () => {
    const { keyboard, text, held } = setup()
    keyboard.keydown(key('a', { metaKey: true }))
    // Browsers can omit the character keyup while Command remains held.
    keyboard.keyup(key('Meta'))
    keyboard.keydown(key('Backspace'))
    expect(text()).toBe('')
    expect([...held]).toEqual([])
  })

  it('deletes once per local repeat without leaving Backspace down remotely', () => {
    const { keyboard, text, held } = setup()
    keyboard.keydown(key('Backspace'))
    for (let i = 0; i < 3; i++) keyboard.keydown(key('Backspace', { repeat: true }))
    expect(text()).toBe('presuffix')
    expect(held.has(0xff08)).toBe(false)
    keyboard.keyup(key('Backspace'))
    expect(text()).toBe('presuffix')
  })

  it('repeats Command deletion without leaving Shift or Control stuck', () => {
    const { keyboard, presses, held } = setup()
    keyboard.keydown(key('Backspace', { metaKey: true }))
    keyboard.keydown(key('Backspace', { metaKey: true, repeat: true }))
    expect(presses.filter(event => event.keysym === 0xff08)).toHaveLength(2)
    expect([...held]).toEqual([])
  })

  it('deletes the previous word with Option+Backspace without activating Linux Alt menus', () => {
    const { keyboard, text, held, send } = setup()
    keyboard.keydown(key('Alt', { altKey: true }))
    keyboard.keydown(key('Backspace', { altKey: true }))
    expect(text()).toBe('suffix')
    expect([...held]).toEqual([])
    keyboard.keyup(key('Backspace', { altKey: true }))
    keyboard.keyup(key('Alt'))
    expect(send.mock.calls.some(([event]) => event.keysym === 0xffe9 && event.down)).toBe(false)
  })

  it('repeats Option word deletion without leaving Control held', () => {
    const { keyboard, presses, held } = setup()
    keyboard.keydown(key('Backspace', { altKey: true }))
    keyboard.keydown(key('Backspace', { altKey: true, repeat: true }))
    const deletions = presses.filter(event => event.keysym === 0xff08)
    expect(deletions).toHaveLength(2)
    expect(deletions.every(event => event.modifiers.includes(0xffe3) && !event.modifiers.includes(0xffe9))).toBe(true)
    expect([...held]).toEqual([])
  })

  it('preserves Linux Alt shortcuts on non-Mac hosts', () => {
    const { keyboard, presses } = setup(false)
    keyboard.keydown(key('Backspace', { altKey: true }))
    expect(presses.find(event => event.keysym === 0xff08)?.modifiers).toContain(0xffe9)
  })

  it('clears a stuck Super/Backspace from the previous pane before accepting new input', () => {
    const { keyboard, held } = setup()
    held.add(0xffeb)
    held.add(0xff08)
    keyboard.keydown(key('a', { metaKey: true }))
    expect([...held]).toEqual([])
  })

  it('preserves physical Control across copy and the following select-all shortcut', () => {
    const { keyboard, copy, held, text } = setup(false)
    keyboard.keydown(key('Control', { ctrlKey: true }))
    keyboard.keydown(key('c', { ctrlKey: true }))
    keyboard.keyup(key('c', { ctrlKey: true }))
    expect(copy).toHaveBeenCalledOnce()
    expect(held.has(0xffe3)).toBe(true)
    keyboard.keydown(key('a', { ctrlKey: true }))
    keyboard.keyup(key('a', { ctrlKey: true }))
    keyboard.keyup(key('Control'))
    keyboard.keydown(key('Backspace'))
    expect(text()).toBe('')
  })

  it('starts the copy gesture once and completes the Command chord without character keyup', () => {
    const { keyboard, copy, held, presses } = setup()
    keyboard.keydown(key('c', { metaKey: true }))
    keyboard.keydown(key('c', { metaKey: true, repeat: true }))
    expect(copy).toHaveBeenCalledOnce()
    expect(presses.find(event => event.keysym === 99)?.modifiers).toContain(0xffe3)
    expect([...held]).toEqual([])
  })

  it('preserves native paste and restores physical modifiers released by backend paste', () => {
    const { keyboard, pasteShortcut, held } = setup(false)
    const event = key('v', { ctrlKey: true, shiftKey: true })
    keyboard.keydown(event)
    expect(event.preventDefault).not.toHaveBeenCalled()
    expect(pasteShortcut).toHaveBeenCalledWith(true)
    held.clear() // Backend performs Ctrl+V with temporary modifier releases.
    keyboard.restoreModifiers()
    expect([...held].sort()).toEqual([0xffe1, 0xffe3])
  })

  it('releases held keys on blur and does not replay them after the pane becomes inactive', () => {
    const { keyboard, held, deactivate, send } = setup(false)
    keyboard.keydown(key('Control', { ctrlKey: true }))
    keyboard.keydown(key('q', { ctrlKey: true }))
    deactivate()
    keyboard.release()
    expect([...held]).toEqual([])
    const count = send.mock.calls.length
    keyboard.keydown(key('Backspace', { repeat: true }))
    expect(send).toHaveBeenCalledTimes(count)
  })
})


describe('keyboard with clipboard input barrier', () => {
  it('restores a physically held modifier after the barrier releases remote keys', async () => {
    const remote = new Set<number>()
    const input = createDisplayInput(event => {
      if (event.type !== 'key') return
      if (event.down) remote.add(event.keysym as number)
      else remote.delete(event.keysym as number)
    }, () => keyboard.reset())
    const keyboard = createDisplayKeyboard({ mac: false, active: () => true, send: input.input, copy() {}, pasteShortcut() {} })
    keyboard.keydown(key('Control', { ctrlKey: true }))
    const sync = Promise.resolve(false)
    input.waitForClipboard(sync)
    input.input({ type: 'pointer', button_mask: 1 })
    input.input({ type: 'pointer', button_mask: 0 })
    await sync
    expect(remote.has(0xffe3)).toBe(false)
    keyboard.keydown(key('a', { ctrlKey: true }))
    expect(remote.has(0xffe3)).toBe(true)
    expect(remote.has(97)).toBe(true)
  })
})
