<script setup lang="ts">
defineProps<{problem:string;readAt:number;loading:boolean}>()
defineEmits<{retry:[]}>()
</script>
<template>
  <div v-if="problem" class="message error" role="alert">{{ problem }}<span v-if="readAt"> · 保留上次读取数据</span><button class="text-button" :disabled="loading" @click="$emit('retry')">重试</button></div>
  <p v-if="readAt" class="read-time">上次读取：{{ new Date(readAt).toLocaleString('zh-CN',{hour12:false}) }}</p>
  <p class="table-hint">表格可左右滑动，查看完整信息和操作。</p>
</template>
