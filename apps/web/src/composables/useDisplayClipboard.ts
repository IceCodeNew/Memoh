import { onScopeDispose, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@felinic/ui'
import type { BotDisplayConnection } from './useBotDisplayConnection'

type ClipboardConnection = Pick<BotDisplayConnection,
  'clipboardText' | 'clipboardRevision' | 'inputChannel' | 'inputReady' | 'pasteText' | 'syncClipboardText'>

export function useDisplayClipboard(connection: ClipboardConnection, isActive: () => boolean) {
  const { t } = useI18n()
  let pending: { received: boolean; text: string | null; resolve: (text: Blob) => void; cancel: () => void } | null = null
  let lastSharedText: string | null = null
  let lastSharedChannel: RTCDataChannel | null = null
  let contextRevision = 0
  let clipboardRevision = 0
  let syncInFlight: Promise<void> | null = null
  let requestReadPermission = false
  let remoteWriteRevision: number | null = null

  function cancelCopy() {
    pending?.cancel()
    pending = null
  }

  // Start the write inside the input gesture. Safari can then wait for the
  // remote copy to arrive without losing the gesture's clipboard permission.
  function prepareRemoteCopy() {
    if (!isActive() || !connection.inputReady()) return
    clipboardRevision++
    cancelCopy()
    if (!navigator.clipboard?.write || typeof ClipboardItem === 'undefined') {
      toast.error(t('chat.display.clipboard.copyDenied'))
      return
    }
    let resolve!: (text: Blob) => void
    let reject!: (reason: Error) => void
    const data = new Promise<Blob>((yes, no) => { resolve = yes; reject = no })
    // A copy with no selection may not produce a new remote value. Never
    // replace the clipboard with an empty or previously copied fallback.
    const timeout = setTimeout(() => {
      if (pending !== request) return
      const received = request.received
      cancelCopy()
      // Ctrl+C can interrupt a terminal process or have no selection to copy.
      if (received) toast.error(t('chat.display.clipboard.copyDenied'))
    }, 5000)
    const request = {
      received: false,
      text: null as string | null,
      resolve: (text: Blob) => { resolve(text) },
      cancel: () => { clearTimeout(timeout); reject(new Error('Copy gesture expired')) },
    }
    pending = request
    // A browser may reject before consuming the ClipboardItem promise.
    void data.catch(() => {})
    try {
      void navigator.clipboard.write([new ClipboardItem({ 'text/plain': data })]).then(() => {
        if (pending !== request) return
        clearTimeout(timeout)
        lastSharedText = request.text
        lastSharedChannel = connection.inputChannel.value
        pending = null
      }).catch(() => {
        if (pending !== request) return
        cancelCopy()
        toast.error(t('chat.display.clipboard.copyDenied'))
      })
    } catch {
      cancelCopy()
      toast.error(t('chat.display.clipboard.copyDenied'))
    }
  }

  watch(connection.clipboardRevision, (revision) => {
    if (!revision || !isActive() || !connection.inputReady()) return
    clipboardRevision++
    lastSharedText = null
    lastSharedChannel = null
    const text = connection.clipboardText.value
    if (pending && !pending.received) {
      pending.received = true
      pending.text = text
      pending.resolve(new Blob([text], { type: 'text/plain' }))
      return
    }
    void syncRemoteClipboard(text)
  }, { flush: 'sync' })

  async function syncRemoteClipboard(text: string) {
    const channel = connection.inputChannel.value
    const context = contextRevision
    const revision = clipboardRevision
    remoteWriteRevision = revision
    const current = () => isActive() && document.hasFocus() && connection.inputReady()
      && channel === connection.inputChannel.value && context === contextRevision && revision === clipboardRevision && !pending
    try {
      // A granted write permission supports menu/application copies without a
      // key gesture. Safari has no such grant; keep its gesture write above.
      if (!navigator.permissions?.query || !current()) return
      const descriptor = { name: 'clipboard-write' as PermissionName, allowWithoutGesture: true }
      const permission = await navigator.permissions.query(descriptor)
      if (permission.state !== 'granted' || !current()) return
      await navigator.clipboard.writeText(text)
      if (!current()) return
      lastSharedText = text
      lastSharedChannel = channel
    } catch {
      // Unsupported/background writes must not prompt or overwrite a newer copy.
    } finally {
      if (remoteWriteRevision === revision) remoteWriteRevision = null
    }
  }

  function pasteText(text: string, shift = false) {
    clipboardRevision++
    cancelCopy()
    if (!isActive() || !connection.inputReady()) return
    if (!connection.pasteText(text, shift)) {
      toast.error(t('chat.display.clipboard.pasteFailed'))
      return
    }
    lastSharedText = text
    lastSharedChannel = connection.inputChannel.value
  }

  // Only supported Permissions API implementations can request a persistent
  // read grant. Safari throws at query(), so it never opens a Paste prompt.
  function syncLocalClipboard(requestPermission = false): Promise<void> {
    if (pending || remoteWriteRevision !== null || !isActive() || !connection.inputReady() || !document.hasFocus()) return Promise.resolve()
    requestReadPermission ||= requestPermission && navigator.userActivation?.isActive === true
    if (syncInFlight) return syncInFlight
    const channel = connection.inputChannel.value
    const revision = contextRevision
    const clipboard = clipboardRevision
    let expired = false
    const current = () => !expired && isActive() && document.hasFocus() && connection.inputReady()
      && connection.inputChannel.value === channel && revision === contextRevision && clipboard === clipboardRevision
    const operation = (async () => {
      try {
        if (!navigator.permissions?.query) return
        const descriptor = { name: 'clipboard-read' as PermissionName, allowWithoutGesture: true }
        const permission = await navigator.permissions.query(descriptor)
        const canRequest = permission.state === 'prompt' && requestReadPermission && navigator.userActivation?.isActive === true
        if ((permission.state !== 'granted' && !canRequest) || !current()) return
        const text = await navigator.clipboard.readText()
        if (!current() || (lastSharedChannel === channel && lastSharedText === text)) return
        if (connection.syncClipboardText(text)) {
          lastSharedText = text
          lastSharedChannel = channel
        }
      } catch {
        // Unsupported permissions and denied reads leave native paste available.
      }
    })()
    let timeout!: ReturnType<typeof setTimeout>
    const deadline = new Promise<void>((resolve) => {
      // Allow time for a first-use permission prompt, then discard late OS
      // reads. Input never waits for this background operation.
      timeout = setTimeout(() => { expired = true; resolve() }, 30_000)
    })
    syncInFlight = Promise.race([operation, deadline]).finally(() => {
      clearTimeout(timeout)
      syncInFlight = null
      requestReadPermission = false
    })
    return syncInFlight
  }

  watch([isActive, connection.inputChannel], () => {
    contextRevision++
    cancelCopy()
  }, { flush: 'sync' })
  onScopeDispose(() => {
    contextRevision++
    cancelCopy()
  })
  return { prepareRemoteCopy, syncLocalClipboard, pasteText }
}
