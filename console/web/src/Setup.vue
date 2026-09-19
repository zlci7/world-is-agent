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

defineProps<{ status: Status }>()
const emit = defineEmits<{ configured: [Status] }>()

const providers = ref<ModelCandidate[]>([])
const optionsProblem = ref<string | null>(null)

const provider = ref('')
const model = ref('')
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
  () => !saving.value && provider.value !== '' && apiKey.value.trim() !== '',
)

onMounted(async () => {
  try {
    const options = await fetchSetupOptions()
    providers.value = options.providers.map((choice) => ({
      provider: choice.provider,
      model: choice.model,
      api_key: '',
    }))
    // The first choice is the provider this build and its release notes lead
    // with; the user can still pick another.
    selectProvider(providers.value[0]?.provider ?? '')
  } catch (error) {
    optionsProblem.value = describe(error)
  }
})

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
  try {
    const updated = await saveModelSetup({
      provider: provider.value,
      model: model.value.trim(),
      api_key: apiKey.value,
    })
    // The credential did its job once the Runtime accepted it; drop it rather
    // than leave it in a component that is about to disappear.
    apiKey.value = ''
    emit('configured', updated)
  } catch (error) {
    // Nothing was written for a failed probe, so the form stays exactly as the
    // user left it and can be corrected and submitted again.
    if (error instanceof ApiError) {
      failure.value = error.message ? `${headline(error.code)}: ${error.message}` : headline(error.code)
    } else {
      failure.value = describe(error)
    }
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <p v-if="status.state === 'blocked'" class="banner banner-error">
    {{ status.reason || 'The Runtime could not prepare its configuration.' }}
    This is not a model setting: the shipped configuration could not be written to the data root
    (<code>{{ status.config_dir }}</code>), so configuring a model here cannot make the Runtime
    ready. Fix that, then start the Runtime again.
  </p>

  <p v-else-if="optionsProblem" class="banner banner-error">
    The Runtime did not report which providers it supports: {{ optionsProblem }}
  </p>

  <section v-else class="card">
    <h2>Set up your model</h2>
    <p class="lead">
      The Runtime drives the agent with a model of your choice. The key is stored on this machine
      only, in the data root, and is never sent anywhere except your provider.
    </p>

    <div class="field">
      <label for="setup-provider">Provider</label>
      <select
        id="setup-provider"
        :value="provider"
        :disabled="saving"
        @change="selectProvider(($event.target as HTMLSelectElement).value)"
      >
        <option v-for="choice in providers" :key="choice.provider" :value="choice.provider">
          {{ choice.provider }}
        </option>
      </select>
    </div>

    <div class="field">
      <label for="setup-model">Model</label>
      <input id="setup-model" v-model="model" :disabled="saving" spellcheck="false" />
    </div>

    <div class="field">
      <label for="setup-key">API Key</label>
      <input
        id="setup-key"
        v-model="apiKey"
        type="password"
        autocomplete="off"
        spellcheck="false"
        :disabled="saving"
        @keyup.enter="submit"
      />
    </div>

    <p v-if="failure" class="problem">{{ failure }}</p>

    <div class="actions">
      <button type="button" :disabled="!canSubmit" @click="submit">
        {{ saving ? 'Testing and saving…' : 'Save and continue' }}
      </button>
      <span v-if="saving" class="muted">
        The Runtime is checking the key with {{ provider }} before it saves anything.
      </span>
      <span v-else-if="!canSubmit" class="muted">An API key is required.</span>
    </div>
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
</style>
