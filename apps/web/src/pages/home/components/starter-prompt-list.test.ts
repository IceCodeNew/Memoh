// @vitest-environment jsdom

import { createApp, h, nextTick, type VNode } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

const uiStubs = vi.hoisted(() => {
  const element = (tag: string) => ({
    inheritAttrs: false,
    setup(_: unknown, context: { attrs: Record<string, unknown>; slots: { default?: () => VNode[] } }) {
      return () => h(tag, context.attrs, context.slots.default?.())
    },
  })
  return {
    Item: {
      inheritAttrs: false,
      props: { as: { type: String, default: 'div' }, size: String },
      setup(props: { as: string }, context: { attrs: Record<string, unknown>; slots: { default?: () => VNode[] } }) {
        return () => h(props.as, context.attrs, context.slots.default?.())
      },
    },
    ItemGroup: element('div'),
    Badge: element('span'),
    Kbd: element('kbd'),
    Skeleton: element('span'),
  }
})

// Native elements keep the assertions about rows, focus and events explicit.
vi.mock('@felinic/ui', () => uiStubs)

import StarterPromptList, { type StarterPromptRow } from './starter-prompt-list.vue'

const rows: StarterPromptRow[] = [
  { key: 'a', title: 'Shrine route', prompt: 'Plan a shrine route', previewable: true },
  { key: 'b', title: 'New releases', prompt: 'List this week\'s releases', previewable: true },
  { key: 'c', title: 'Web search', prompt: 'Search the web', previewable: false, badge: 'Needs web search' },
]

describe('StarterPromptList', () => {
  let app: ReturnType<typeof createApp> | undefined
  let root: HTMLDivElement | undefined

  afterEach(() => {
    app?.unmount()
    root?.remove()
    app = undefined
    root = undefined
  })

  function mount(list: StarterPromptRow[] | null) {
    const select = vi.fn()
    const preview = vi.fn()
    root = document.createElement('div')
    document.body.append(root)
    app = createApp({ render: () => h(StarterPromptList, { rows: list, onSelect: select, onPreview: preview }) })
    app.config.globalProperties.$t = (key: string) => key
    app.mount(root)
    const buttons = () => [...root!.querySelectorAll<HTMLButtonElement>('button[data-starter-prompt]')]
    return { select, preview, buttons }
  }

  it('renders three busy skeleton rows while the set is loading', () => {
    mount(null)
    expect(root!.querySelector('[aria-busy="true"]')).not.toBeNull()
    expect(root!.querySelectorAll('[data-starter-prompt]')).toHaveLength(3)
    expect(root!.querySelectorAll('button')).toHaveLength(0)
  })

  it('previews a row on hover and focus, and clears it on leave and blur', async () => {
    const { preview, buttons } = mount(rows)
    const [first] = buttons()
    first!.dispatchEvent(new MouseEvent('mouseenter'))
    first!.dispatchEvent(new MouseEvent('mouseleave'))
    first!.dispatchEvent(new FocusEvent('focus'))
    first!.dispatchEvent(new FocusEvent('blur'))
    await nextTick()
    expect(preview.mock.calls).toEqual([['Plan a shrine route'], [null], ['Plan a shrine route'], [null]])
  })

  it('never previews a row that cannot be prefilled', () => {
    const { preview, buttons } = mount(rows)
    buttons()[2]!.dispatchEvent(new MouseEvent('mouseenter'))
    buttons()[2]!.dispatchEvent(new FocusEvent('focus'))
    expect(preview).not.toHaveBeenCalled()
  })

  it('shows the badge instead of the use hint on a row that has one', () => {
    const { buttons } = mount(rows)
    expect(buttons()[2]!.textContent).toContain('Needs web search')
    expect(buttons()[2]!.querySelector('[data-starter-prompt-hint]')).toBeNull()
    expect(buttons()[0]!.querySelector('[data-starter-prompt-hint]')).not.toBeNull()
  })

  it('selects the clicked row', () => {
    const { select, buttons } = mount(rows)
    buttons()[1]!.click()
    expect(select).toHaveBeenCalledWith(rows[1])
  })

  it('moves focus with the arrow keys, Home and End, wrapping at the ends', () => {
    const { buttons } = mount(rows)
    const press = (key: string) => document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
    buttons()[0]!.focus()
    press('ArrowDown')
    expect(document.activeElement).toBe(buttons()[1])
    press('End')
    expect(document.activeElement).toBe(buttons()[2])
    press('ArrowDown')
    expect(document.activeElement).toBe(buttons()[0])
    press('ArrowUp')
    expect(document.activeElement).toBe(buttons()[2])
    press('Home')
    expect(document.activeElement).toBe(buttons()[0])
  })
})
