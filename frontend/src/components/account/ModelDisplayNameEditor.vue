<template>
  <div v-if="rows.length > 0" class="space-y-2">
    <div
      v-for="(row, index) in rows"
      :key="getRowKey(row)"
      class="flex items-center gap-2"
    >
      <input
        v-model="row.model"
        type="text"
        data-testid="model-display-name-model"
        class="input flex-1"
        :placeholder="t('admin.accounts.modelDisplayName.modelPlaceholder')"
      />
      <svg
        class="h-4 w-4 flex-shrink-0 text-gray-400"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M14 5l7 7m0 0l-7 7m7-7H3"
        />
      </svg>
      <input
        v-model="row.displayName"
        type="text"
        data-testid="model-display-name-value"
        class="input flex-1"
        :placeholder="t('admin.accounts.modelDisplayName.displayNamePlaceholder')"
      />
      <button
        type="button"
        class="rounded-lg p-2 text-red-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20"
        @click="removeRow(index)"
      >
        <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
          />
        </svg>
      </button>
    </div>
  </div>

  <button
    type="button"
    data-testid="model-display-name-add"
    class="w-full rounded-lg border-2 border-dashed border-gray-300 px-4 py-2 text-gray-600 transition-colors hover:border-gray-400 hover:text-gray-700 dark:border-dark-500 dark:text-gray-400 dark:hover:border-dark-400 dark:hover:text-gray-300"
    @click="addRow"
  >
    <svg class="mr-1 inline h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
      <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
    </svg>
    {{ t('admin.accounts.modelDisplayName.addRow') }}
  </button>

  <p class="text-xs text-gray-500 dark:text-gray-400">
    {{ t('admin.accounts.modelDisplayName.emptyValueHint') }}
  </p>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { createStableObjectKeyResolver } from '@/utils/stableObjectKey'
import type { ModelDisplayNameRow } from './credentialsBuilder'

const props = defineProps<{
  rows: ModelDisplayNameRow[]
}>()

const emit = defineEmits<{
  (e: 'update:rows', rows: ModelDisplayNameRow[]): void
}>()

const { t } = useI18n()

const getRowKey = createStableObjectKeyResolver<ModelDisplayNameRow>('model-display-name-row')

const addRow = () => {
  emit('update:rows', [...props.rows, { model: '', displayName: '' }])
}

const removeRow = (index: number) => {
  emit('update:rows', props.rows.filter((_, i) => i !== index))
}
</script>
