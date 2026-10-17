<script setup lang="ts">
defineProps<{ name: string }>()
const paths: Record<string,string> = {
  leaf:'M12 3C6 3 3 6 3 10c0 4 3 7 7 7 5 0 9-5 10-14-3 1-5 0-8 0Z M5 21 15 10',
  compass:'m12 2 3 7 7 3-7 3-3 7-3-7-7-3 7-3 3-7Z',
  home:'m3 10 9-7 9 7 M5 9v11h14V9 M10 20v-7h4v7',
  book:'M4 4h12a3 3 0 0 1 3 3v14H7a3 3 0 0 1-3-3V4Z M4 17h15 M8 8h7 M8 11h5',
  settings:'M4 7h16 M4 17h16 M8 4v6 M16 14v6',
  history:'M4 5v5h5 M5 9a8 8 0 1 1-1 6 M12 7v5l3 2',
  chevron:'m9 6 6 6-6 6', back:'m14 6-6 6 6 6 M8 12h12',
  refresh:'M20 7v5h-5 M4 17v-5h5 M5.5 8a7 7 0 0 1 11.6-3L20 8 M4 16l2.9 3A7 7 0 0 0 18.5 16',
  check:'m5 12 4 4L19 6',clock:'M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z M12 7v5l3 2',
  shield:'m12 3 8 3v6c0 4-4 7-8 9-4-2-8-5-8-9V6l8-3Z m-4 9 3 3 5-6',
  world:'M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z M3 12h18 M12 3c4 5 4 13 0 18-4-5-4-13 0-18Z',
  pause:'M8 5v14 M16 5v14',play:'m8 4 12 8-12 8V4Z', close:'m6 6 12 12 M18 6 6 18',
}
</script>
<template><svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path :d="paths[name] || paths.compass" /></svg></template>
