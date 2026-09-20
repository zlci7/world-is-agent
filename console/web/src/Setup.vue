<script setup lang="ts">
/** The first-run card. It exists for the one state nothing else can resolve: a
 *  Runtime that has no model configuration yet.
 *
 *  There is one button and no test button. The Runtime probes the exact
 *  parameters it is about to write, so a separate test would only add a second
 *  real model call and a second state that can disagree with what was saved. */
import { computed, onMounted, ref } from 'vue'
import { fetchSetupOptions, saveModelSetup } from './api'
import { ApiError, type ModelCandidate, type Status } from './types'

const props = defineProps<{
  status: Status
  mode: 'initial' | 'settings'
  busy: boolean
  beginStatusMutation: () => number
  acceptStatus: (status: Status, request: number) => void
}>()
const emit = defineEmits<{
  mutationFailed: [unknown, number]
  readFailed: [unknown]
  saved: []
  cancelled: []
}>()

const providers = ref<ModelCandidate[]>([])
const optionsProblem = ref<string | null>(null)
const optionsLoading = ref(false)

const provider = ref('')
const model = ref('')
const baseUrl = ref('')
const apiKey = ref('')
const saving = ref(false)
const failure = ref('')

/** Failure codes the Runtime can answer with. The probe's own message is shown
 *  as the detail because it is written to be safe for a browser and never
 *  carries the credential. */
const failureHeadlines: Record<string, string> = {
  invalid_configuration: 'Check the configuration',
  authentication_failed: 'Authentication failed',
  network_unavailable: 'Network unavailable',
  provider_error: 'The provider answered with an error',
  setup_blocked: 'The Runtime cannot be configured',
  model_setup_failed: 'The configuration was not accepted',
}

const canSubmit = computed(
  () => !saving.value && !props.busy && provider.value !== '' && apiKey.value.trim() !== '',
)

onMounted(loadOptions)

async function loadOptions() {
  optionsLoading.value = true
  optionsProblem.value = null
  try {
    const options = await fetchSetupOptions()
    providers.value = options.providers.map((choice) => ({
      provider: choice.provider,
      model: choice.model,
      api_key: '',
    }))
    // The first choice is the provider this build and its release notes lead
    // with; the user can still pick another.
    if (props.mode === 'settings') {
      provider.value = props.status.model?.provider ?? providers.value[0]?.provider ?? ''
      model.value = props.status.model?.model ?? ''
      baseUrl.value = ''
    } else {
      selectProvider(providers.value[0]?.provider ?? '')
    }
  } catch (error) {
    optionsProblem.value = describe(error)
    emit('readFailed', error)
  } finally {
    optionsLoading.value = false
  }
}

function describe(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function selectProvider(next: string) {
  provider.value = next
  const choice = providers.value.find((candidate) => candidate.provider === next)
  model.value = choice?.model ?? ''
}

function headline(code: string): string {
  return failureHeadlines[code] ?? 'The configuration was not accepted'
}

async function submit() {
  if (!canSubmit.value) {
    return
  }
  saving.value = true
  failure.value = ''
  const request = props.beginStatusMutation()
  try {
    const updated = await saveModelSetup({
      provider: provider.value,
      model: model.value.trim(),
      base_url: baseUrl.value.trim(),
      api_key: apiKey.value,
    })
    // The credential did its job once the Runtime accepted it; drop it rather
    // than leave it in a component that is about to disappear.
    apiKey.value = ''
    props.acceptStatus(updated, request)
    emit('saved')
  } catch (error) {
    // Nothing was written for a failed probe, so the form stays exactly as the
    // user left it and can be corrected and submitted again.
    if (error instanceof ApiError) {
      failure.value = error.message ? `${headline(error.code)}: ${error.message}` : headline(error.code)
    } else {
      failure.value = describe(error)
    }
    emit('mutationFailed', error, request)
  } finally {
    saving.value = false
  }
}

function cancel() {
  apiKey.value = ''
  failure.value = ''
  emit('cancelled')
}
</script>

<template>
  <section class="card">
    <h2>{{ mode === 'settings' ? 'Model settings' : 'Set up your model' }}</h2>
    <template v-if="optionsProblem">
      <p class="banner banner-error">
        The Runtime did not report which providers it supports: {{ optionsProblem }}
      </p>
      <div class="actions">
        <button type="button" :disabled="optionsLoading || busy" @click="loadOptions">
          {{ optionsLoading ? 'Retrying…' : 'Retry' }}
        </button>
        <button v-if="mode === 'settings'" type="button" class="secondary" :disabled="busy" @click="cancel">
          Cancel
        </button>
      </div>
    </template>
    <template v-else>
    <p class="lead">
      The Runtime drives the agent with a model of your choice. The key is stored on this machine
      only, in the data root, and is never sent anywhere except your provider.
    </p>

    <div class="field">
      <label for="setup-provider">Provider</label>
      <select
        id="setup-provider"
        :value="provider"
        :disabled="saving || busy"
        @change="selectProvider(($event.target as HTMLSelectElement).value)"
      >
        <option v-for="choice in providers" :key="choice.provider" :value="choice.provider">
          {{ choice.provider }}
        </option>
      </select>
    </div>

    <div class="field">
      <label for="setup-model">Model</label>
      <input id="setup-model" v-model="model" :disabled="saving || busy" spellcheck="false" />
    </div>

    <div class="field">
      <label for="setup-base-url">Base URL</label>
      <div class="field-control">
        <input id="setup-base-url" v-model="baseUrl" :disabled="saving || busy" spellcheck="false" />
        <small>Leave empty to use the provider default.</small>
      </div>
    </div>

    <div class="field">
      <label for="setup-key">API Key</label>
      <input
        id="setup-key"
        v-model="apiKey"
        type="password"
        autocomplete="off"
        spellcheck="false"
        :disabled="saving || busy"
        @keyup.enter="submit"
      />
    </div>

    <p v-if="failure" class="problem">{{ failure }}</p>

    <div class="actions">
      <button type="button" :disabled="!canSubmit" @click="submit">
        {{ saving ? 'Testing and applying…' : (mode === 'settings' ? 'Apply settings' : 'Save and continue') }}
      </button>
      <button v-if="mode === 'settings'" type="button" class="secondary" :disabled="saving || busy" @click="cancel">
        Cancel
      </button>
      <span v-if="saving" class="muted">
        The Runtime is checking the key with {{ provider }} before it saves anything.
      </span>
      <span v-else-if="!canSubmit" class="muted">An API key is required.</span>
    </div>
    </template>
  </section>
</template>

<style scoped>
.lead {
  margin: 0 0 16px;
  color: var(--muted);
}

.field {
  display: grid;
  grid-template-columns: 90px minmax(0, 1fr);
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}

.field label {
  color: var(--muted);
  font-size: 13px;
}

.field input,
.field select {
  width: 100%;
  max-width: 420px;
  padding: 6px 8px;
  border: 1px solid var(--line);
  border-radius: 5px;
  background: var(--bg);
  color: var(--ink);
  font: inherit;
}

.field-control {
  display: grid;
  gap: 3px;
}

.field-control small {
  color: var(--muted);
}

.field input:disabled,
.field select:disabled {
  opacity: 0.6;
}

.actions {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 16px;
  flex-wrap: wrap;
}

button {
  padding: 7px 14px;
  border: 1px solid transparent;
  border-radius: 5px;
  background: var(--accent);
  color: var(--panel);
  font: inherit;
  font-weight: 600;
  cursor: pointer;
}

button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

.secondary {
  border-color: var(--line);
  background: transparent;
  color: var(--ink);
}
</style>
