<template>
  <div v-if="rows.length > 0" class="space-y-3">
    <div
      v-for="(row, index) in rows"
      :key="getRowKey(row)"
      class="rounded-lg border border-gray-200 bg-gray-50/40 p-3 dark:border-dark-600 dark:bg-dark-800/40"
    >
      <div class="flex items-center gap-2">
        <input
          v-model="row.model"
          type="text"
          class="input flex-1"
          data-testid="reasoning-effort-override-model"
          :placeholder="t('admin.accounts.reasoningEffortOverride.modelPlaceholder')"
        />
        <button
          type="button"
          class="rounded-lg p-2 text-red-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20"
          :aria-label="t('admin.accounts.reasoningEffortOverride.removeRow')"
          @click="removeRow(index)"
        >
          <Icon name="trash" size="sm" />
        </button>
      </div>

      <div class="mt-3 flex flex-wrap gap-1.5">
        <button
          v-for="level in reasoningEffortLevels"
          :key="level.value"
          type="button"
          :data-testid="`reasoning-effort-override-level-${level.value}`"
          :aria-pressed="row.levels.includes(level.value)"
          :class="[
            'rounded-md border px-2 py-1 text-xs font-medium transition-colors',
            row.levels.includes(level.value)
              ? 'border-primary-300 bg-primary-100 text-primary-700 dark:border-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
              : 'border-gray-300 bg-white text-gray-600 hover:bg-gray-100 dark:border-dark-500 dark:bg-dark-700 dark:text-gray-300 dark:hover:bg-dark-600'
          ]"
          @click="toggleLevel(index, level.value)"
        >
          {{ level.label }}
        </button>
      </div>

      <div v-if="row.levels.length > 0" class="mt-3">
        <label class="input-label" :for="`${getRowKey(row)}-default`">
          {{ t('admin.accounts.reasoningEffortOverride.defaultLevel') }}
        </label>
        <Select
          :id="`${getRowKey(row)}-default`"
          :model-value="row.defaultLevel"
          :options="defaultOptions(row)"
          :aria-label="t('admin.accounts.reasoningEffortOverride.defaultLevel')"
          :searchable="false"
          @update:model-value="updateDefault(index, $event)"
        />
      </div>
    </div>
  </div>

  <button
    type="button"
    data-testid="reasoning-effort-override-add"
    class="w-full rounded-lg border-2 border-dashed border-gray-300 px-4 py-2 text-gray-600 transition-colors hover:border-gray-400 hover:text-gray-700 dark:border-dark-500 dark:text-gray-400 dark:hover:border-dark-400 dark:hover:text-gray-300"
    @click="addRow"
  >
    <Icon name="plus" size="sm" class="mr-1 inline" />
    {{ t('admin.accounts.reasoningEffortOverride.addRow') }}
  </button>

  <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
    {{ t('admin.accounts.reasoningEffortOverride.hint') }}
  </p>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import type { SelectOption } from '@/components/common/Select.vue'
import { createStableObjectKeyResolver } from '@/utils/stableObjectKey'
import {
  REASONING_EFFORT_LEVELS,
  normalizeReasoningEffortLevel,
  type ReasoningEffortOverrideRow
} from './credentialsBuilder'

const props = defineProps<{
  rows: ReasoningEffortOverrideRow[]
}>()

const emit = defineEmits<{
  (e: 'update:rows', rows: ReasoningEffortOverrideRow[]): void
}>()

const { t } = useI18n()

const reasoningEffortLevels = REASONING_EFFORT_LEVELS
const getRowKey = createStableObjectKeyResolver<ReasoningEffortOverrideRow>(
  'reasoning-effort-override-row'
)

const defaultOptions = (row: ReasoningEffortOverrideRow): SelectOption[] =>
  REASONING_EFFORT_LEVELS.filter((level) => row.levels.includes(level.value))

const addRow = () => {
  emit('update:rows', [...props.rows, { model: '', levels: [], defaultLevel: '' }])
}

const removeRow = (index: number) => {
  emit(
    'update:rows',
    props.rows.filter((_, i) => i !== index)
  )
}

const toggleLevel = (index: number, level: string) => {
  emit(
    'update:rows',
    props.rows.map((row, i) => {
      if (i !== index) return row
      const levels = row.levels.includes(level)
        ? row.levels.filter((value) => value !== level)
        : [...row.levels, level]
      const defaultLevel =
        row.defaultLevel && levels.includes(row.defaultLevel) ? row.defaultLevel : levels[0] || ''
      return { ...row, levels, defaultLevel }
    })
  )
}

const updateDefault = (index: number, level: string | number | boolean | null) => {
  emit(
    'update:rows',
    props.rows.map((row, i) =>
      i === index
        ? { ...row, defaultLevel: normalizeReasoningEffortLevel(level == null ? '' : String(level)) }
        : row
    )
  )
}
</script>
