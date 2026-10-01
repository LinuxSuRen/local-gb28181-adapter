<script setup>
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { snapshotUrl } from '../api'

const props = defineProps({
  visible: { type: Boolean, default: false },
  camera: { type: Object, default: null }
})
const emit = defineEmits(['update:visible'])

const show = computed({
  get: () => props.visible,
  set: (v) => emit('update:visible', v)
})

const url = ref('')
const loading = ref(false)

function reload() {
  if (!props.camera || !props.camera.id) return
  url.value = snapshotUrl(props.camera.id)
  loading.value = true
}

watch(
  () => props.visible,
  (v) => {
    if (v) reload()
  }
)

function onLoad() {
  loading.value = false
}

function onError() {
  loading.value = false
  ElMessage.error('快照加载失败，请确认摄像头处于运行状态')
}
</script>

<template>
  <el-dialog v-model="show" :title="`${camera && camera.name ? camera.name : ''} 快照`" width="720px">
    <div v-loading="loading" class="snapshot-wrap">
      <img v-if="url" :src="url" alt="摄像头快照" @load="onLoad" @error="onError" />
    </div>
    <template #footer>
      <el-button :loading="loading" @click="reload">
        <el-icon><Refresh /></el-icon>刷新
      </el-button>
      <el-button type="primary" @click="show = false">关闭</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.snapshot-wrap {
  min-height: 240px;
  display: flex;
  justify-content: center;
  align-items: center;
  background: #000;
  border-radius: 4px;
}
.snapshot-wrap img {
  max-width: 100%;
  max-height: 70vh;
  display: block;
}
</style>
