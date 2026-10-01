<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getSystem, getCameras, getSessions, deleteCamera } from './api'
import SystemCard from './components/SystemCard.vue'
import PlatformCard from './components/PlatformCard.vue'
import CameraTable from './components/CameraTable.vue'
import CameraDialog from './components/CameraDialog.vue'
import SnapshotDialog from './components/SnapshotDialog.vue'
import PtzDrawer from './components/PtzDrawer.vue'

const system = ref(null)
const cameras = ref([])
const sessions = ref([])
const loading = ref(false)
let pollTimer = null

const dialogVisible = ref(false)
const editingCamera = ref(null)
const snapshotVisible = ref(false)
const snapshotCamera = ref(null)
const ptzVisible = ref(false)
const ptzCamera = ref(null)

async function loadAll() {
  loading.value = true
  try {
    const [sys, cams, sss] = await Promise.all([getSystem(), getCameras(), getSessions()])
    system.value = sys
    cameras.value = Array.isArray(cams) ? cams : []
    sessions.value = Array.isArray(sss) ? sss : []
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  loadAll()
  // 注册状态与点播会话定时刷新
  pollTimer = setInterval(loadAll, 5000)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})

function openCreate() {
  editingCamera.value = null
  dialogVisible.value = true
}

function openEdit(camera) {
  editingCamera.value = camera
  dialogVisible.value = true
}

function openSnapshot(camera) {
  snapshotCamera.value = camera
  snapshotVisible.value = true
}

function openPtz(camera) {
  ptzCamera.value = camera
  ptzVisible.value = true
}

async function handleRemove(camera) {
  try {
    await deleteCamera(camera.id)
    ElMessage.success('已删除')
    await loadAll()
  } catch (e) {
    ElMessage.error(e.message)
  }
}
</script>

<template>
  <div class="page">
    <header class="page-header">
      <h1>local-gb28181-adapter 管理台</h1>
      <p>将本地摄像头以国标 GB/T 28181 设备形式接入上级平台</p>
    </header>

    <SystemCard :system="system" :sessions="sessions" class="block" />

    <PlatformCard class="block" @saved="loadAll" />

    <CameraTable
      class="block"
      :cameras="cameras"
      :loading="loading"
      :show-channel="true"
      @add="openCreate"
      @refresh="loadAll"
      @edit="openEdit"
      @snapshot="openSnapshot"
      @ptz="openPtz"
      @remove="handleRemove"
    />

    <CameraDialog v-model:visible="dialogVisible" :camera="editingCamera" @saved="loadAll" />
    <SnapshotDialog v-model:visible="snapshotVisible" :camera="snapshotCamera" />
    <PtzDrawer v-model:visible="ptzVisible" :camera="ptzCamera" />
  </div>
</template>

<style>
body {
  margin: 0;
  background: #f5f7fa;
}
</style>

<style scoped>
.page {
  max-width: 1200px;
  margin: 0 auto;
  padding: 24px 16px 48px;
}
.page-header h1 {
  margin: 0 0 4px;
  font-size: 22px;
  color: #303133;
}
.page-header p {
  margin: 0;
  color: #909399;
  font-size: 14px;
}
.block {
  margin-top: 16px;
}
</style>
