import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, ref, shallowRef } from 'vue'
import { useDisplayClipboard } from './useDisplayClipboard'

const showError = vi.hoisted(() => vi.fn())
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@felinic/ui', () => ({ toast: { error: showError } }))

describe('clipboard sync without extra controls', () => {
  const scopes: ReturnType<typeof effectScope>[] = []
  const write = vi.fn<(items: { text: Promise<Blob> }[]) => Promise<void>>()
  const readText = vi.fn<() => Promise<string>>()
  const writeText = vi.fn<() => Promise<void>>()
  const queryPermission = vi.fn()
  const hasFocus = vi.fn(() => true)

  function setup() {
    const scope = effectScope()
    scopes.push(scope)
    const active = ref(true)
    const connection = {
      clipboardText: ref(''), clipboardRevision: ref(0),
      inputChannel: shallowRef({ readyState: 'open' } as RTCDataChannel),
      inputReady: () => connection.inputChannel.value?.readyState === 'open',
      pasteText: vi.fn(() => true),
      syncClipboardText: vi.fn(() => true),
    }
    const clipboard = scope.run(() => useDisplayClipboard(connection, () => active.value))!
    return { active, connection, clipboard }
  }

  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    vi.stubGlobal('navigator', { clipboard: { write, readText, writeText }, permissions: { query: queryPermission } })
    vi.stubGlobal('document', { hasFocus })
    hasFocus.mockReturnValue(true)
    queryPermission.mockImplementation(async (descriptor: { name: string }) => ({ state: descriptor.name === 'clipboard-read' ? 'granted' : 'prompt' }))
    vi.stubGlobal('ClipboardItem', class {
      text: Promise<Blob>
      constructor(data: Record<string, Promise<Blob>>) { this.text = data['text/plain']! }
    })
    write.mockImplementation(async items => { await items[0]!.text })
    writeText.mockResolvedValue()
  })

  afterEach(() => {
    scopes.splice(0).forEach(scope => scope.stop())
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('requests the write during the gesture, then resolves with the later remote copy', async () => {
    const { clipboard, connection } = setup()
    clipboard.prepareRemoteCopy()
    expect(write).toHaveBeenCalledOnce()
    connection.clipboardText.value = '中文\nremote copy'
    connection.clipboardRevision.value++
    const blob = await write.mock.calls[0]![0][0]!.text
    expect(await blob.text()).toBe('中文\nremote copy')
    expect(writeText).not.toHaveBeenCalled()
  })

  it('leaves the system clipboard alone when remote selections change without a copy shortcut', () => {
    const { connection } = setup()
    connection.clipboardText.value = 'old text with a timestamp'
    connection.clipboardRevision.value++
    expect(write).not.toHaveBeenCalled()
    expect(writeText).not.toHaveBeenCalled()
  })

  it('expires a copy without overwriting the existing clipboard', async () => {
    const { clipboard } = setup()
    clipboard.prepareRemoteCopy()
    const data = write.mock.calls[0]![0][0]!.text
    const rejected = expect(data).rejects.toThrow('expired')
    await vi.advanceTimersByTimeAsync(5000)
    await rejected
    expect(writeText).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })

  it('reports Safari rejection even after the remote text has arrived', async () => {
    write.mockImplementationOnce(async items => {
      await items[0]!.text
      throw new Error('NotAllowedError')
    })
    const { clipboard, connection } = setup()
    clipboard.prepareRemoteCopy()
    connection.clipboardText.value = 'fresh remote text'
    connection.clipboardRevision.value++
    await vi.advanceTimersByTimeAsync(0)
    expect(showError).toHaveBeenCalledExactlyOnceWith('chat.display.clipboard.copyDenied')
  })

  it('uses text from the native paste event without reading through the async Clipboard API', () => {
    const { clipboard, connection } = setup()
    clipboard.pasteText('本地\ntext', true)
    expect(connection.pasteText).toHaveBeenCalledExactlyOnceWith('本地\ntext', true)
    expect(readText).not.toHaveBeenCalled()
  })

  it('synchronizes with an existing read grant without executing a paste shortcut', async () => {
    const { clipboard, connection } = setup()
    readText.mockResolvedValue('本机 新文本')
    await clipboard.syncLocalClipboard()
    await clipboard.syncLocalClipboard()
    expect(queryPermission).toHaveBeenCalledWith({ name: 'clipboard-read', allowWithoutGesture: true })
    expect(connection.syncClipboardText).toHaveBeenCalledExactlyOnceWith('本机 新文本')
    expect(connection.pasteText).not.toHaveBeenCalled()
  })

  it('resynchronizes local text after a remote clipboard change invalidates the cache', async () => {
    const { clipboard, connection } = setup()
    readText.mockResolvedValue('local A')
    await clipboard.syncLocalClipboard()
    connection.clipboardText.value = 'remote B'
    connection.clipboardRevision.value++
    await vi.advanceTimersByTimeAsync(0)
    await clipboard.syncLocalClipboard()
    expect(connection.syncClipboardText.mock.calls).toEqual([['local A'], ['local A']])
  })

  it('writes remote application/menu copies automatically with a write grant', async () => {
    queryPermission.mockResolvedValue({ state: 'granted' })
    const { connection } = setup()
    connection.clipboardText.value = '复制自远程菜单'
    connection.clipboardRevision.value++
    await vi.advanceTimersByTimeAsync(0)
    expect(writeText).toHaveBeenCalledExactlyOnceWith('复制自远程菜单')
    expect(write).not.toHaveBeenCalled()
    expect(connection.pasteText).not.toHaveBeenCalled()
  })

  it('does not echo the old local clipboard back while a remote menu copy is being written', async () => {
    queryPermission.mockResolvedValue({ state: 'granted' })
    let finishWrite!: () => void
    writeText.mockImplementationOnce(() => new Promise(resolve => { finishWrite = resolve }))
    readText.mockResolvedValue('old local text')
    const { clipboard, connection } = setup()
    connection.clipboardText.value = 'new remote copy'
    connection.clipboardRevision.value++
    await Promise.resolve()
    await clipboard.syncLocalClipboard()
    expect(readText).not.toHaveBeenCalled()
    finishWrite()
    await vi.advanceTimersByTimeAsync(0)
    readText.mockResolvedValue('new remote copy')
    await clipboard.syncLocalClipboard()
    expect(connection.syncClipboardText).not.toHaveBeenCalled()
  })

  it('discards stale remote copies while checking permission', async () => {
    let grant!: (permission: { state: string }) => void
    queryPermission.mockImplementationOnce(() => new Promise(resolve => { grant = resolve }))
    const { connection } = setup()
    connection.clipboardText.value = 'old remote copy'
    connection.clipboardRevision.value++
    connection.clipboardText.value = 'new remote copy'
    connection.clipboardRevision.value++
    grant({ state: 'granted' })
    await vi.advanceTimersByTimeAsync(0)
    expect(writeText).not.toHaveBeenCalled()
  })

  it('does not write remote copies to an inactive or unfocused window', async () => {
    queryPermission.mockResolvedValue({ state: 'granted' })
    const { connection, active } = setup()
    hasFocus.mockReturnValue(false)
    connection.clipboardText.value = 'background copy'
    connection.clipboardRevision.value++
    hasFocus.mockReturnValue(true)
    active.value = false
    connection.clipboardRevision.value++
    await vi.advanceTimersByTimeAsync(0)
    expect(writeText).not.toHaveBeenCalled()
  })

  it('does not let a slow local read overwrite a newer native paste or remote copy', async () => {
    const { clipboard, connection } = setup()
    let resolve!: (text: string) => void
    readText.mockImplementation(() => new Promise(yes => { resolve = yes }))
    const first = clipboard.syncLocalClipboard()
    await Promise.resolve()
    clipboard.pasteText('new native paste')
    resolve('stale local read')
    await first
    const second = clipboard.syncLocalClipboard()
    await Promise.resolve()
    connection.clipboardText.value = 'new remote copy'
    connection.clipboardRevision.value++
    resolve('another stale local read')
    await second
    expect(connection.syncClipboardText).not.toHaveBeenCalled()
  })

  it.each(['prompt', 'denied', 'unsupported'])('never opens a paste prompt when permission is %s', async (state) => {
    const { clipboard, connection } = setup()
    if (state === 'unsupported') queryPermission.mockRejectedValueOnce(new TypeError('Unsupported permission'))
    else queryPermission.mockResolvedValueOnce({ state })
    await clipboard.syncLocalClipboard()
    expect(readText).not.toHaveBeenCalled()
    expect(connection.syncClipboardText).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })

  it('requests a supported read permission only during an explicit desktop interaction', async () => {
    vi.stubGlobal('navigator', { clipboard: { write, readText, writeText }, permissions: { query: queryPermission }, userActivation: { isActive: true } })
    queryPermission.mockResolvedValue({ state: 'prompt' })
    readText.mockResolvedValue('clipboard after browser approval')
    const { clipboard, connection } = setup()
    // Enter/focus can start first; the click may join its permission query.
    const focus = clipboard.syncLocalClipboard()
    const click = clipboard.syncLocalClipboard(true)
    await Promise.all([focus, click])
    expect(readText).toHaveBeenCalledOnce()
    expect(connection.syncClipboardText).toHaveBeenCalledWith('clipboard after browser approval')
  })

  it.each(['denied', 'unsupported'])('never requests a read when permission is %s even on click', async (state) => {
    vi.stubGlobal('navigator', { clipboard: { write, readText, writeText }, permissions: { query: queryPermission }, userActivation: { isActive: true } })
    if (state === 'unsupported') queryPermission.mockRejectedValueOnce(new TypeError('Unsupported permission'))
    else queryPermission.mockResolvedValueOnce({ state })
    const { clipboard } = setup()
    await clipboard.syncLocalClipboard(true)
    expect(readText).not.toHaveBeenCalled()
  })

  it('discards pending reads after switching panes or reconnecting', async () => {
    const { clipboard, connection, active } = setup()
    let resolve!: (text: string) => void
    readText.mockImplementation(() => new Promise(yes => { resolve = yes }))
    const paste = clipboard.syncLocalClipboard()
    await Promise.resolve()
    active.value = false
    resolve('wrong pane')
    await paste
    active.value = true
    const reconnectPaste = clipboard.syncLocalClipboard()
    await Promise.resolve()
    connection.inputChannel.value = { readyState: 'open' } as RTCDataChannel
    resolve('old connection')
    await reconnectPaste
    expect(connection.syncClipboardText).not.toHaveBeenCalled()
  })

  it('keeps native paste available when an automatic read fails', async () => {
    const { clipboard, connection } = setup()
    readText.mockRejectedValueOnce(new Error('NotAllowedError'))
    await clipboard.syncLocalClipboard()
    expect(showError).not.toHaveBeenCalled()
    expect(connection.syncClipboardText).not.toHaveBeenCalled()
    clipboard.pasteText('native paste')
    expect(connection.pasteText).toHaveBeenCalledWith('native paste', false)
  })

  it('discards text from an OS read that outlives its deadline', async () => {
    const { clipboard, connection } = setup()
    let resolve!: (text: string) => void
    readText.mockImplementationOnce(() => new Promise(yes => { resolve = yes }))
    const sync = clipboard.syncLocalClipboard()
    await vi.advanceTimersByTimeAsync(30_000)
    await sync
    resolve('late text')
    await vi.advanceTimersByTimeAsync(0)
    expect(connection.syncClipboardText).not.toHaveBeenCalled()
  })
  it('fails the click barrier on a denied granted read or rejected channel send', async () => {
    const { clipboard, connection } = setup()
    readText.mockRejectedValueOnce(new Error('NotAllowedError'))
    expect(await clipboard.syncLocalClipboard(true)).toBe(false)
    readText.mockResolvedValue('fresh')
    connection.syncClipboardText.mockReturnValueOnce(false)
    expect(await clipboard.syncLocalClipboard(true)).toBe(false)
  })

  it('bounds a click read and prevents late text from reaching the remote clipboard', async () => {
    const { clipboard, connection } = setup()
    let resolve!: (text: string) => void
    readText.mockImplementationOnce(() => new Promise(yes => { resolve = yes }))
    const sync = clipboard.syncLocalClipboard(true)
    await vi.advanceTimersByTimeAsync(1500)
    expect(await sync).toBe(false)
    resolve('late text')
    await vi.advanceTimersByTimeAsync(0)
    expect(connection.syncClipboardText).not.toHaveBeenCalled()
  })

  it('preserves a queued click and copy shortcut when that copy supersedes the local read', async () => {
    const { clipboard, connection } = setup()
    let resolve!: (text: string) => void
    readText.mockImplementationOnce(() => new Promise(yes => { resolve = yes }))
    const sync = clipboard.syncLocalClipboard(true)
    await Promise.resolve()
    clipboard.prepareRemoteCopy()
    resolve('old local clipboard')
    expect(await sync).toBe(true)
    expect(connection.syncClipboardText).not.toHaveBeenCalled()
    connection.clipboardText.value = 'new remote selection'
    connection.clipboardRevision.value++
    const blob = await write.mock.calls[0]![0][0]!.text
    expect(await blob.text()).toBe('new remote selection')
  })

})
