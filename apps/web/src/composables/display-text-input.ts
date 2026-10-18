type LocalKeyEvent = Pick<KeyboardEvent, 'key' | 'code' | 'keyCode' | 'isComposing'>

// Legacy KeyboardEvent code for a key handled by an input method. Some
// browsers report it before compositionstart/isComposing becomes available.
const IME_PROCESS_KEY_CODE = 229
const COMPOSITION_SETTLE_MS = 50

// The textarea owns local IME preedit. Only its committed value crosses RFB;
// candidate navigation and the Enter/Space that confirms it stay local.
export function createDisplayTextInput(options: {
  active: () => boolean
  release: () => void
  text: (value: string) => void
}) {
  let composing = false
  let lastCommit: string | null = null
  let canceled = false
  let commits: string[] = []
  let commitTimer: ReturnType<typeof setTimeout> | undefined

  function flushCommits() {
    clearTimeout(commitTimer)
    commitTimer = undefined
    const pending = commits
    commits = []
    if (!canceled && options.active()) {
      for (const text of pending) options.text(text)
    }
  }

  function queueCommit(value: string) {
    commits.push(value)
    if (commitTimer !== undefined) return
    // macOS can deliver compositionend in a separate task before the mouse
    // event that caused it (observed ~14 ms before pointerdown). One task is
    // not a cancellation boundary. Allow that native event batch to settle;
    // an ordinary next key flushes first so editing never overtakes the text.
    commitTimer = setTimeout(flushCommits, COMPOSITION_SETTLE_MS)
  }

  const localKeys = new Set<string>()

  function keydown(event: LocalKeyEvent) {
    // Let the input method consume preedit/candidate keys without preventing
    // their browser defaults or forwarding them to the remote application.
    const local = composing || event.isComposing || event.keyCode === IME_PROCESS_KEY_CODE || ['Process', 'Dead'].includes(event.key)
    // A new ordinary key ends the window for deduplicating the last commit.
    if (!local) {
      flushCommits()
      lastCommit = null
      canceled = false
    }
    if (local) localKeys.add(event.code || event.key)
    return local
  }

  function keyup(event: LocalKeyEvent) {
    // The Enter/Space that confirms a candidate can be released after
    // compositionend. Remember its keydown so that keyup also stays local.
    const local = localKeys.delete(event.code || event.key)
    return local || composing || event.isComposing || event.keyCode === IME_PROCESS_KEY_CODE
  }

  function start() {
    canceled = false
    composing = options.active()
    lastCommit = null
    // Composition takes ownership of subsequent keys; do not leave keys
    // pressed remotely while their releases are being handled locally.
    if (composing) options.release()
  }

  function end(value: string) {
    const commit = composing && !canceled && options.active()
    composing = false
    // Composition-owned keydowns must not affect a later ordinary key if the
    // browser does not deliver their keyup after the IME closes.
    localKeys.clear()
    lastCommit = value
    if (commit && value) queueCommit(value)
  }

  function input(value: string, isComposing: boolean) {
    if (composing || isComposing) return false
    // Chromium may emit the final input after compositionend; Safari may
    // emit it before. Neither order should type the committed text twice.
    const duplicate = lastCommit === value
    lastCommit = null
    if (!canceled && options.active() && value && !duplicate) {
      queueCommit(value)
    }
    return true
  }

  function cancel() {
    // Blur, hiding the pane, or replacing the connection abandons preedit.
    // A later compositionend must not send it to a different remote focus.
    canceled ||= composing || commits.length > 0
    composing = false
    commits = []
    clearTimeout(commitTimer)
    commitTimer = undefined
    lastCommit = null
    localKeys.clear()
  }

  return { keydown, keyup, start, end, input, cancel }
}
