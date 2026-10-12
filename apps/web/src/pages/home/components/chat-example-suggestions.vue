<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useEventListener, usePreferredReducedMotion } from '@vueuse/core'
import { X } from 'lucide-vue-next'
import { Button, TextButton, toast } from '@felinic/ui'
import { useChatExampleAction, useChatExampleEntries } from '@/components/chat-examples/use-chat-example-action'
import { useChatExamplesUi } from '@/components/chat-examples/use-chat-examples-ui'
import { useStarterPromptsQuery } from '@/composables/api/useStarterPrompts'
import { useComposerPrefill } from '@/composables/useComposerPrefill'
import { localizeText } from '@/lib/chat-examples/select'
import { COMPOSER_PREVIEW_LANDING_MS, type ComposerPreview } from '../composables/useComposerPreview'
import StarterPromptList, { type StarterPromptRow } from './starter-prompt-list.vue'

/**
 * Three suggestions under the welcome composer. They are the bot's own
 * starter prompts for this user; until the first set arrives the rows show a
 * skeleton, and when the bot has no set (no title model, or generation
 * failed) three built-in examples drawn at random take their place.
 * "Refresh" asks for a different generated set, or draws three other
 * built-in examples; the gallery of every example lives behind the sidebar
 * entry instead. Hiding the section is remembered on this device;
 * while hidden, the composer's context row shows the entry that brings it
 * back. Hidden by the welcome column's height tiers (style.css) before
 * anything else yields.
 *
 * Hovering or focusing a row previews its prompt in the composer through the
 * `preview` model, and while a preview shows, Enter picks that row (the "↵"
 * hint on the row says so) wherever focus is. Picking the row that is being
 * previewed first "lands" the preview (it inks into the real text) and then
 * prefills, so the text never blinks out and back in.
 */
const props = defineProps<{
  botId: string
  /**
   * The composer holds no text of the user's own (empty, or an unedited
   * prefill), so a row may preview into it and Enter may pick a row.
   */
  composerReplaceable: boolean
}>()

const preview = defineModel<ComposerPreview | null>('preview', { default: null })

const { locale, t } = useI18n()
const { welcomeDismissed } = useChatExamplesUi()
const { prefillComposer } = useComposerPrefill()
const reducedMotion = usePreferredReducedMotion()

const { data: starterSet, status: starterStatus, refresh: refreshStarterSet, refreshing } = useStarterPromptsQuery(() => props.botId)

// A new seed per mounted welcome view: every new chat draws a different three,
// while this one stays put through re-renders and the capability probe.
// Refreshing the built-in examples draws a new seed.
const seed = ref(Math.random())
const entries = useChatExampleEntries(() => props.botId, () => ({ surface: 'welcome', limit: 3, spreadCategories: true, seed: seed.value }))
const { run } = useChatExampleAction(() => props.botId)

/**
 * The rows to show: null while a set is on its way (the first one, or a
 * refresh; skeleton), the generated set, or the built-in examples. An
 * example the bot lacks a capability for carries a badge and opens settings
 * instead of prefilling, so it is not previewable.
 */
const rows = computed<StarterPromptRow[] | null>(() => {
  if (refreshing.value) return null
  const generated = starterSet.value?.items ?? []
  if (generated.length) {
    return generated.map((item, index) => ({ key: `starter:${index}`, title: item.title, prompt: item.prompt, previewable: true }))
  }
  if (!starterSet.value && starterStatus.value === 'pending') return null
  return (entries.value ?? []).map(({ example, missing }) => ({
    key: `example:${example.id}`,
    title: localizeText(example.title, locale.value),
    prompt: localizeText(example.prompt, locale.value),
    badge: missing[0] ? t('chatExamples.needs', { capability: t(`chatExamples.capability.${missing[0]}`) }) : undefined,
    previewable: missing.length === 0,
  }))
})

/** Whether the composer is currently showing the preview. */
const previewShown = computed(() => props.composerReplaceable && preview.value !== null)

const visible = computed(() => !welcomeDismissed.value && (rows.value === null || rows.value.length > 0))
const generated = computed(() => (starterSet.value?.items.length ?? 0) > 0)

async function refresh() {
  preview.value = null
  if (!generated.value) {
    seed.value = Math.random()
    return
  }
  if (!(await refreshStarterSet())) toast.error(t('chatExamples.refreshFailed'))
}

function setPreview(prompt: string | null) {
  if (preview.value?.landing) return
  preview.value = prompt ? { text: prompt, landing: false } : null
}

/**
 * Enter picks the previewed row. Captured on the window so it runs before the
 * composer (which would send) and before a focused row's own activation.
 */
useEventListener(window, 'keydown', (event: KeyboardEvent) => {
  if (event.key !== 'Enter' || event.isComposing || event.shiftKey || event.altKey || event.ctrlKey || event.metaKey) return
  const previewed = preview.value
  if (!previewShown.value || !previewed || previewed.landing) return
  const row = rows.value?.find(candidate => candidate.previewable && candidate.prompt === previewed.text)
  if (!row) return
  event.preventDefault()
  event.stopPropagation()
  void select(row)
}, { capture: true })

async function select(row: StarterPromptRow) {
  if (row.key.startsWith('example:')) {
    const entry = entries.value?.find(({ example }) => `example:${example.id}` === row.key)
    if (!entry) return
    if (entry.missing.length) {
      await run(entry.example, entry.missing)
      return
    }
  }
  if (previewShown.value && preview.value?.text === row.prompt && reducedMotion.value !== 'reduce') {
    preview.value = { text: row.prompt, landing: true }
    await new Promise(resolve => setTimeout(resolve, COMPOSER_PREVIEW_LANDING_MS))
  }
  try {
    await prefillComposer(row.prompt, { botId: props.botId })
  }
  finally {
    preview.value = null
  }
}
</script>

<template>
  <section
    v-if="visible"
    data-welcome-suggestions
    class="@container pointer-events-auto mx-auto w-full max-w-[44rem] px-4 pb-8 sm:px-6 lg:px-10 md:-translate-x-0.5"
    :aria-label="$t('chatExamples.tryThese')"
  >
    <!-- pb-8 keeps the welcome column's 2rem rhythm below the last row: in a
         short pane the anchor spacer shrinks first, so the rows stop a gap
         short of the pane edge instead of resting on it. -->
    <!-- Same box model as the rows below (1px edge + px-4), so the label
         starts on the rows' text column and the close glyph ends on their
         trailing column. The section shares the composer's 2px optical nudge.
         The label follows the section-title standard (text-label, medium). -->
    <div class="mb-1 flex items-center justify-between gap-2 border border-transparent px-4">
      <h2 class="text-label font-medium text-muted-foreground">
        {{ $t('chatExamples.tryThese') }}
      </h2>
      <div class="flex items-center gap-1">
        <!-- Same type as the composer's session controls one row up
             (text-label, regular weight), not TextButton's 14px medium. -->
        <TextButton
          type="button"
          class="text-label font-normal"
          :disabled="rows === null"
          @click="refresh"
        >
          {{ $t('chatExamples.refresh') }}
        </TextButton>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          shape="circle"
          class="-mr-2 text-muted-foreground"
          :aria-label="$t('chatExamples.dismiss')"
          @click="welcomeDismissed = true"
        >
          <X class="size-4" />
        </Button>
      </div>
    </div>
    <StarterPromptList
      :rows="rows"
      :enter-hint="composerReplaceable"
      @select="select"
      @preview="setPreview"
    />
  </section>
</template>
