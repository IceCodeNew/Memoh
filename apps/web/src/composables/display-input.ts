type Input = Record<string, unknown>

// Keep a click (including its release and any subsequent input) behind the
// clipboard read. Never replay an incomplete click after a failed read.
export function createDisplayInput(send: (input: Input) => void, failed: () => void) {
  let batch: { inputs: Input[] } | null = null
  let suppressPointer = false
  let pointerMask = 0
  let pointer: Input | null = null
  const keys = new Set<number>()

  function forward(input: Input) {
    if (input.type === 'key') {
      if (input.down) keys.add(input.keysym as number)
      else keys.delete(input.keysym as number)
    }
    if (input.type === 'pointer') pointer = input
    send(input)
  }

  function release() {
    for (const keysym of keys) send({ type: 'key', keysym, down: false })
    keys.clear()
    if (pointer) send({ ...pointer, button_mask: 0 })
    pointer = null
  }

  function cancel() {
    batch = null
    suppressPointer = pointerMask !== 0
    release()
  }

  function waitForClipboard(sync: Promise<boolean>) {
    if (batch) return
    const current = { inputs: [] as Input[] }
    batch = current
    void sync.then((ok) => {
      if (batch !== current) return
      batch = null
      if (ok) current.inputs.forEach(forward)
      else {
        suppressPointer = pointerMask !== 0
        release()
        failed()
      }
    })
  }

  function input(value: Input) {
    if (value.type === 'pointer') pointerMask = value.button_mask as number
    if (value.type === 'pointer' && suppressPointer) {
      if (value.button_mask !== 0) return
      suppressPointer = false
    }
    if (!batch) return forward(value)
    const last = batch.inputs.at(-1)
    // Mouse movement may be frequent during a slow permission prompt. Keep
    // transitions, but coalesce movement with an unchanged button mask.
    const previous = batch.inputs.at(-2)
    if (last?.type === 'pointer' && previous?.type === 'pointer' && value.type === 'pointer'
      && previous.button_mask === value.button_mask && last.button_mask === value.button_mask) {
      batch.inputs[batch.inputs.length - 1] = value
    } else if (batch.inputs.length < 256) {
      batch.inputs.push(value)
    } else {
      cancel()
      failed()
    }
  }

  return { input, waitForClipboard, cancel }
}
