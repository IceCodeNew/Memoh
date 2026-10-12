<script setup lang="ts">
import { computed } from 'vue'
import { Sparkles, X } from 'lucide-vue-next'
import { ActionCard, Button, Skeleton, TextButton } from '@felinic/ui'
import ChatExampleCard from '@/components/chat-examples/chat-example-card.vue'
import { useChatExampleAction, useChatExampleEntries } from '@/components/chat-examples/use-chat-example-action'
import { useChatExamplesUi } from '@/components/chat-examples/use-chat-examples-ui'
import { useStarterPromptsQuery } from '@/composables/api/useStarterPrompts'
import { useComposerPrefill } from '@/composables/useComposerPrefill'

/**
 * Three suggestions under the welcome composer, with "See all" opening the
 * full gallery. They are the bot's own starter prompts for this user; until
 * the first set arrives the row shows skeleton cards, and when the bot has no
 * set (no title model, or generation failed) it shows three built-in examples
 * drawn at random instead. Hiding the row is remembered on this device; while
 * hidden, the composer's context row shows the entry that brings it back.
 * Hidden by the welcome column's height tiers (style.css) before anything
 * else yields.
 */
const props = defineProps<{ botId: string }>()

const { welcomeDismissed, openGallery } = useChatExamplesUi()
const { prefillComposer } = useComposerPrefill()

const { data: starterSet, status: starterStatus } = useStarterPromptsQuery(() => props.botId)
const starterPrompts = computed(() => starterSet.value?.items ?? [])
const awaitingFirstSet = computed(() => !starterSet.value && starterStatus.value === 'pending')

// A new seed per mounted welcome view: every new chat draws a different three,
// while this one stays put through re-renders and the capability probe.
const seed = Math.random()
const entries = useChatExampleEntries(() => props.botId, { surface: 'welcome', limit: 3, spreadCategories: true, seed })
const { run } = useChatExampleAction(() => props.botId)

const visible = computed(() =>
  !welcomeDismissed.value && (starterPrompts.value.length > 0 || awaitingFirstSet.value || !!entries.value?.length))
</script>

<template>
  <section
    v-if="visible"
    data-welcome-suggestions
    class="@container pointer-events-auto mx-auto w-full max-w-[44rem] px-4 sm:px-6 lg:px-10 md:-translate-x-0.5"
    :aria-label="$t('chatExamples.tryThese')"
  >
    <!-- Same box model as the cards below (1px edge + px-4), so the label
         starts on the cards' icon column and the close glyph ends on their
         chevron column. The section shares the composer's 2px optical nudge.
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
          @click="openGallery"
        >
          {{ $t('chatExamples.viewAll') }}
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
    <!-- Narrow panes scroll one row sideways instead of stacking, so the
         strip keeps a single card's height under the composer. -->
    <div
      class="flex snap-x snap-mandatory gap-2 overflow-x-auto scrollbar-none @xl:grid @xl:grid-cols-3 @xl:overflow-visible"
      :aria-busy="awaitingFirstSet || undefined"
    >
      <template v-if="starterPrompts.length">
        <ActionCard
          v-for="(item, index) in starterPrompts"
          :key="`${index}:${item.title}`"
          as="button"
          type="button"
          bordered
          class="w-4/5 shrink-0 snap-start @xl:w-full"
          :title="item.title"
          :description="item.prompt"
          @click="prefillComposer(item.prompt, { botId })"
        >
          <template #icon>
            <Sparkles />
          </template>
        </ActionCard>
      </template>
      <template v-else-if="awaitingFirstSet">
        <!-- Mirrors a bordered ActionCard with a description (px-4 py-3.5,
             a 20px title line, mt-0.5 and an 16px description line) so the
             swap to real cards keeps the row's height. -->
        <div
          v-for="i in 3"
          :key="i"
          class="flex w-4/5 shrink-0 snap-start items-center gap-3 rounded-menu-shell border border-border bg-card px-4 py-3.5 @xl:w-full"
        >
          <Skeleton class="size-4 shrink-0 rounded-sm" />
          <div class="min-w-0 flex-1">
            <div class="flex h-5 items-center">
              <Skeleton class="h-3.5 w-1/2" />
            </div>
            <div class="mt-0.5 flex h-4 items-center">
              <Skeleton class="h-3 w-4/5" />
            </div>
          </div>
        </div>
      </template>
      <template v-else>
        <ChatExampleCard
          v-for="entry in entries"
          :key="entry.example.id"
          class="w-4/5 shrink-0 snap-start @xl:w-full"
          :example="entry.example"
          :missing="entry.missing"
          @select="run(entry.example, entry.missing)"
        />
      </template>
    </div>
  </section>
</template>
