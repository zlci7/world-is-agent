<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import UiIcon from '../UiIcon.vue'
import { ApiError } from '../types'
import { request, type UserSession } from './api'

const props=defineProps<{session:UserSession|null;resetFlow:boolean;accountBusy:boolean;externalProblem:string}>()
const emit=defineEmits<{authenticated:[UserSession];logout:[];expired:[]}>()
type Mode='login'|'register'|'forgot'|'reset'|'verify'
type Challenge={challenge_id:string;expires_at:number;retry_at:number}
const mode=ref<Mode>(props.resetFlow?'reset':props.session?'verify':'login')
const email=ref(''),password=ref(''),confirmation=ref(''),invitation=ref(''),code=ref('')
const busy=ref(false),sending=ref(false),problem=ref(''),notice=ref(''),challenge=ref<Challenge|null>(null)
const now=ref(Date.now()),retryAt=ref(0),token=ref('')
const title=computed(()=>({login:'登录我的游戏',register:'加入邀请内测',forgot:'找回密码',reset:'设置新密码',verify:'验证你的邮箱'}[mode.value]))
const wait=computed(()=>Math.max(0,Math.ceil((retryAt.value-now.value)/1000)))
const controller=new AbortController()
const storageKey='wia.pending-email-verification'
const countdown=window.setInterval(()=>now.value=Date.now(),1000)

if(props.resetFlow){token.value=new URLSearchParams(window.location.hash.slice('#reset?'.length)).get('token')??'';history.replaceState(null,'',location.pathname+location.search)}
try{
  const pending=JSON.parse(sessionStorage.getItem(storageKey)??'null') as {email:string;mode:Mode;challenge:Challenge}|null
  if(pending&&pending.mode===(props.session?'verify':'register')&&pending.challenge.expires_at*1000>Date.now()&&!props.resetFlow&&(!props.session||props.session.user.email===pending.email)){
    email.value=pending.email;mode.value=props.session?'verify':'register';challenge.value=pending.challenge;retryAt.value=pending.challenge.retry_at*1000
  }
}catch{/* Browser storage is optional for completing verification. */}
watch(()=>props.session,value=>{if(value&&!value.user.email_verified_at){if(mode.value!=='verify')changeMode('verify');email.value=value.user.email}else if(!value&&mode.value==='verify')changeMode('login')})
watch(email,(value,old)=>{if(value.toLowerCase().trim()!==old.toLowerCase().trim()){challenge.value=null;code.value='';retryAt.value=0;clearPending()}})
function clearPending(){try{sessionStorage.removeItem(storageKey)}catch{/* Verification works without browser storage. */}}
function changeMode(next:Mode){mode.value=next;problem.value='';notice.value='';password.value='';confirmation.value='';code.value='';challenge.value=null;retryAt.value=0;clearPending()}
function signOut(){clearPending();emit('logout')}
function fail(error:unknown){if(controller.signal.aborted)return;if(error instanceof ApiError&&error.status===401&&props.session){emit('expired');return}problem.value=error instanceof Error?error.message:'操作失败，请稍后重试'}
async function sendCode(){
  sending.value=true;problem.value='';notice.value=''
  try{
    const verifying=mode.value==='verify'
    const result=await request<Challenge>(verifying?'/api/me/email-verification':'/api/registration-code',{method:'POST',csrf:props.session?.csrf,body:verifying?{}:{email:email.value,invitation:invitation.value},signal:controller.signal})
    challenge.value=result;now.value=Date.now();retryAt.value=result.retry_at*1000;notice.value='验证码已发送，10 分钟内有效，请检查收件箱和垃圾邮件。'
    try{sessionStorage.setItem(storageKey,JSON.stringify({email:verifying?props.session?.user.email:email.value.toLowerCase().trim(),mode:mode.value,challenge:result}))}catch{/* Form remains usable when browser storage is unavailable. */}
  }catch(error){if(error instanceof ApiError&&error.status===429){now.value=Date.now();retryAt.value=now.value+60000}fail(error)}finally{sending.value=false}
}
async function submit(){
  busy.value=true;problem.value='';notice.value=''
  try{
    if(mode.value==='forgot'){
      await request('/api/password-resets',{method:'POST',body:{email:email.value},signal:controller.signal});notice.value='如果邮箱已注册并通过验证，你将收到重置邮件。请检查收件箱和垃圾邮件。';now.value=Date.now();retryAt.value=now.value+60000;return
    }
    if(mode.value==='reset'){
      if(password.value!==confirmation.value){problem.value='两次输入的密码不一致';return}
      await request('/api/password-resets/complete',{method:'POST',body:{token:token.value,password:password.value},signal:controller.signal});token.value='';changeMode('login');notice.value='密码已更新，请重新登录。原 WIA Key 已失效，登录后请创建新密钥。';emit('expired');return
    }
    const verifying=mode.value==='verify',registering=mode.value==='register'
    if((verifying||registering)&&!challenge.value){problem.value='请先获取邮箱验证码';return}
    const proof={challenge_id:challenge.value?.challenge_id,code:code.value}
    const result=await request<UserSession>(verifying?'/api/me/email-verification/confirm':registering?'/api/invitations/redeem':'/api/session',{method:'POST',csrf:props.session?.csrf,signal:controller.signal,body:verifying?proof:{email:email.value,password:password.value,...(registering?{invitation:invitation.value,...proof}:{})}})
    if(controller.signal.aborted)return
    clearPending();password.value='';invitation.value='';code.value='';emit('authenticated',result)
  }catch(error){fail(error)}finally{busy.value=false}
}
onUnmounted(()=>{controller.abort();window.clearInterval(countdown);token.value='';password.value='';code.value=''})
</script>

<template>
  <main class="hosted-auth surface">
    <div class="auth-title"><span class="brand-mark"><UiIcon name="compass" /></span><span>World Is Agent</span></div>
    <h1>{{ title }}</h1>
    <p v-if="mode==='login'">让角色记住与你一起经历的事。</p>
    <p v-else-if="mode==='register'">验证邮箱后，领取个人密钥连接游戏。</p>
    <p v-else-if="mode==='verify'" class="auth-email">{{ session?.user.email }}<br />完成验证后，继续使用你的账号和游戏记录。</p>
    <p v-else-if="mode==='forgot'">使用已验证的账号邮箱接收重置链接。</p>
    <p v-else>设置后原网页会话和游戏密钥将失效。</p>
    <p v-if="problem||externalProblem" class="notice problem" role="alert">{{ problem||externalProblem }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <form @submit.prevent="submit">
      <div v-if="mode!=='verify'&&mode!=='reset'" class="field"><label for="auth-email">邮箱</label><input id="auth-email" v-model="email" type="email" autocomplete="email" required maxlength="254" :disabled="busy||sending" /></div>
      <div v-if="mode==='register'" class="field"><label for="auth-invitation">邀请码</label><input id="auth-invitation" v-model="invitation" autocomplete="off" required maxlength="160" :disabled="busy||sending" /></div>
      <div v-if="mode==='register'||mode==='verify'" class="field">
        <label for="auth-code">邮箱验证码</label>
        <div class="verification-control"><input id="auth-code" v-model="code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" maxlength="6" required :disabled="busy||sending" /><button class="secondary" type="button" :disabled="busy||sending||wait>0||(mode==='register'&&(!email||!invitation))" @click="sendCode">{{ sending?'正在发送…':wait>0?`${wait} 秒后重发`:challenge?'重新发送':'获取验证码' }}</button></div>
        <small>六位验证码，10 分钟内有效。</small>
      </div>
      <div v-if="mode==='login'||mode==='register'||mode==='reset'" class="field"><label for="auth-password">{{ mode==='reset'?'新密码':'密码' }}</label><input id="auth-password" v-model="password" type="password" :autocomplete="mode==='login'?'current-password':'new-password'" required :minlength="mode==='login'?undefined:12" maxlength="128" :disabled="busy||sending" /><small v-if="mode!=='login'">12–128 个字符，可使用密码管理器。</small></div>
      <div v-if="mode==='reset'" class="field"><label for="auth-confirmation">再次输入新密码</label><input id="auth-confirmation" v-model="confirmation" type="password" autocomplete="new-password" required minlength="12" maxlength="128" :disabled="busy" /></div>
      <button class="auth-submit" type="submit" :disabled="busy||sending||(mode==='forgot'&&wait>0)||(mode==='reset'&&!token)">{{ busy?'正在提交…':mode==='forgot'?(wait>0?`${wait} 秒后重新申请`:'发送重置邮件'):mode==='reset'?'保存新密码':mode==='verify'?'验证邮箱':mode==='register'?'注册并进入':'登录' }}</button>
    </form>
    <div v-if="mode==='login'" class="auth-links"><button class="text-link" :disabled="busy" @click="changeMode('register')">使用邀请码注册</button><button class="text-link" :disabled="busy" @click="changeMode('forgot')">忘记密码</button></div>
    <div v-else-if="mode==='verify'" class="auth-switch"><button class="text-link" :disabled="busy||sending||accountBusy" @click="signOut">{{ accountBusy?'正在退出…':'退出当前账号' }}</button></div>
    <div v-else class="auth-switch"><button class="text-link" :disabled="busy||sending" @click="changeMode('login')">返回登录</button></div>
    <p class="auth-help">游戏在你的电脑运行，经历和预约保存在账号下。</p>
  </main>
</template>
