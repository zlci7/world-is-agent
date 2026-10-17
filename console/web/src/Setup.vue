<script setup lang="ts">
/** The first-run card. It exists for the one state nothing else can resolve: a
 *  Runtime that has no model configuration yet.
 *
 *  There is one button and no test button. The Runtime probes the exact
 *  parameters it is about to write, so a separate test would only add a second
 *  real model call and a second state that can disagree with what was saved. */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { fetchSetupOptions, saveModelSetup } from './api'
import { type ModelCandidate, type Status } from './types'
import { errorText } from './i18n/zh-CN'
import UiIcon from './UiIcon.vue'

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

const canSubmit = computed(
  () => !saving.value && !props.busy && provider.value !== '' && apiKey.value.trim() !== '',
)

onMounted(loadOptions)
onUnmounted(() => { apiKey.value = '' })

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
      const current = providers.value.find(choice => choice.provider === props.status.model?.provider)
      provider.value = current?.provider ?? providers.value[0]?.provider ?? ''
      model.value = current ? props.status.model?.model ?? '' : providers.value[0]?.model ?? ''
      baseUrl.value = ''
    } else {
      selectProvider(providers.value[0]?.provider ?? '')
    }
  } catch (error) {
    optionsProblem.value = errorText(error)
    emit('readFailed', error)
  } finally {
    optionsLoading.value = false
  }
}

function selectProvider(next: string) {
  provider.value = next
  const choice = providers.value.find((candidate) => candidate.provider === next)
  model.value = choice?.model ?? ''
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
    failure.value = errorText(error)
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
  <section class="surface settings-panel">
    <h2 class="panel-title"><UiIcon name="settings" />{{ mode === 'settings' ? '模型设置' : '配置模型' }}</h2>
    <p class="panel-description">新配置通过连接检查后生效，检查失败时保留当前运行模型。接口密钥保存在本机。</p>
    <template v-if="optionsProblem"><p class="notice problem" role="alert">{{ optionsProblem }}</p><div class="actions"><button :disabled="optionsLoading || busy" @click="loadOptions">{{ optionsLoading ? '正在读取…' : '重新读取服务商' }}</button><button v-if="mode === 'settings'" class="secondary" :disabled="busy" @click="cancel">取消修改</button></div></template>
    <form v-else @submit.prevent="submit">
      <div class="field"><label for="setup-provider">服务商</label><select id="setup-provider" :value="provider" :disabled="saving || busy" @change="selectProvider(($event.target as HTMLSelectElement).value)"><option v-for="choice in providers" :key="choice.provider" :value="choice.provider">{{ choice.provider }}</option></select></div>
      <div class="field"><label for="setup-model">模型名称</label><input id="setup-model" v-model="model" :disabled="saving || busy" spellcheck="false" /></div>
      <div class="field"><label for="setup-key">接口密钥</label><input id="setup-key" v-model="apiKey" type="password" autocomplete="off" spellcheck="false" :disabled="saving || busy" required /><small>网页不显示已保存的密钥，请输入本次配置使用的密钥。</small></div>
      <div class="field"><label for="setup-base-url">接口地址（可选）</label><input id="setup-base-url" v-model="baseUrl" :disabled="saving || busy" spellcheck="false" /><small>留空时使用服务商默认地址。</small></div>
      <p v-if="failure" class="notice problem" role="alert">{{ failure }}</p>
      <div class="actions"><button type="submit" :disabled="!canSubmit">{{ saving ? '正在检查并应用…' : '检查并保存' }}<UiIcon name="check" /></button><button v-if="mode === 'settings'" type="button" class="secondary" :disabled="saving || busy" @click="cancel">取消修改</button><span v-if="saving" role="status">正在检查模型连接，通过后保存。</span></div>
    </form>
  </section>
</template>
