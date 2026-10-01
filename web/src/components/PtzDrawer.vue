<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { getPtz, sendPtz } from '../api'

const props = defineProps({
  visible: { type: Boolean, default: false },
  camera: { type: Object, default: null }
})
const emit = defineEmits(['update:visible'])

const show = computed({
  get: () => props.visible,
  set: (v) => emit('update:visible', v)
})

const cameraId = computed(() => (props.camera && props.camera.id) || null)

const state = ref({ pan: 0, tilt: 0, zoom: 0, moving: false, presets: [] })
const loading = ref(false)
const absPan = ref(0)
const absTilt = ref(0)
const absZoom = ref(0)
const presetName = ref('')
const pressing = ref(false)
let pollTimer = null

// 3×3 方向键盘：对角线各 0.5，中间为停止按钮
const pad = [
  [
    { pan: -0.5, tilt: 0.5, icon: 'TopLeft' },
    { pan: 0, tilt: 0.5, icon: 'Top' },
    { pan: 0.5, tilt: 0.5, icon: 'TopRight' }
  ],
  [
    { pan: -0.5, tilt: 0, icon: 'Left' },
    null,
    { pan: 0.5, tilt: 0, icon: 'Right' }
  ],
  [
    { pan: -0.5, tilt: -0.5, icon: 'BottomLeft' },
    { pan: 0, tilt: -0.5, icon: 'Bottom' },
    { pan: 0.5, tilt: -0.5, icon: 'BottomRight' }
  ]
]

function fmt(n) {
  return Number(n || 0).toFixed(2)
}

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

function syncPolling() {
  if (state.value.moving && !pollTimer) {
    pollTimer = setInterval(refresh, 2000)
  } else if (!state.value.moving) {
    stopPolling()
  }
}

async function refresh() {
  if (!cameraId.value) return
  loading.value = true
  try {
    const data = await getPtz(cameraId.value)
    state.value = {
      pan: (data && data.pan) || 0,
      tilt: (data && data.tilt) || 0,
      zoom: (data && data.zoom) || 0,
      moving: !!(data && data.moving),
      presets: (data && data.presets) || []
    }
    absPan.value = state.value.pan
    absTilt.value = state.value.tilt
    absZoom.value = state.value.zoom
    syncPolling()
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

async function op(body, successMsg) {
  if (!cameraId.value) return false
  try {
    await sendPtz(cameraId.value, body)
    if (successMsg) ElMessage.success(successMsg)
    await refresh()
    return true
  } catch (e) {
    ElMessage.error(e.message)
    return false
  }
}

function startContinuous(vec) {
  pressing.value = true
  op({
    op: 'continuous',
    pan: vec.pan || 0,
    tilt: vec.tilt || 0,
    zoom: vec.zoom || 0
  })
}

function stopContinuous() {
  if (!pressing.value) return
  pressing.value = false
  op({ op: 'stop' })
}

function stopNow() {
  pressing.value = false
  op({ op: 'stop' })
}

function gotoAbsolute() {
  op({ op: 'absolute', pan: absPan.value, tilt: absTilt.value, zoom: absZoom.value }, '已转到目标位置')
}

async function savePreset() {
  if (!presetName.value.trim()) {
    ElMessage.warning('请输入预置位名称')
    return
  }
  const ok = await op({ op: 'preset_set', preset_name: presetName.value.trim() }, '已设置预置位')
  if (ok) presetName.value = ''
}

function gotoPreset(token) {
  op({ op: 'preset_goto', preset_token: token }, '已调用预置位')
}

function removePreset(token) {
  op({ op: 'preset_remove', preset_token: token }, '已删除预置位')
}

watch(
  () => props.visible,
  (v) => {
    if (v) {
      refresh()
    } else {
      stopPolling()
      pressing.value = false
    }
  }
)

onBeforeUnmount(stopPolling)
</script>

<template>
  <el-drawer v-model="show" :title="`${camera && camera.name ? camera.name : ''} PTZ 试控`" size="440px">
    <div v-loading="loading" class="ptz-body">
      <el-descriptions :column="2" border size="small">
        <el-descriptions-item label="Pan">{{ fmt(state.pan) }}</el-descriptions-item>
        <el-descriptions-item label="Tilt">{{ fmt(state.tilt) }}</el-descriptions-item>
        <el-descriptions-item label="Zoom">{{ fmt(state.zoom) }}</el-descriptions-item>
        <el-descriptions-item label="状态">
          <el-tag :type="state.moving ? 'success' : 'info'" size="small">
            {{ state.moving ? '运动中' : '静止' }}
          </el-tag>
        </el-descriptions-item>
      </el-descriptions>

      <el-divider content-position="left">方向控制</el-divider>
      <div class="dpad">
        <template v-for="(row, ri) in pad" :key="ri">
          <template v-for="(cell, ci) in row" :key="ri + '-' + ci">
            <el-button
              v-if="cell"
              class="pad-btn"
              @mousedown="startContinuous(cell)"
              @mouseup="stopContinuous"
              @mouseleave="stopContinuous"
              @touchstart.prevent="startContinuous(cell)"
              @touchend="stopContinuous"
            >
              <el-icon><component :is="cell.icon" /></el-icon>
            </el-button>
            <el-button v-else class="pad-btn" type="danger" plain @click="stopNow">
              <el-icon><SwitchButton /></el-icon>
            </el-button>
          </template>
        </template>
      </div>
      <div class="zoom-row">
        <el-button
          @mousedown="startContinuous({ zoom: 0.5 })"
          @mouseup="stopContinuous"
          @mouseleave="stopContinuous"
          @touchstart.prevent="startContinuous({ zoom: 0.5 })"
          @touchend="stopContinuous"
        >
          <el-icon><ZoomIn /></el-icon>放大
        </el-button>
        <el-button
          @mousedown="startContinuous({ zoom: -0.5 })"
          @mouseup="stopContinuous"
          @mouseleave="stopContinuous"
          @touchstart.prevent="startContinuous({ zoom: -0.5 })"
          @touchend="stopContinuous"
        >
          <el-icon><ZoomOut /></el-icon>缩小
        </el-button>
      </div>
      <p class="tip">按住方向按钮持续转动，松开停止；对角线为组合方向</p>

      <el-divider content-position="left">绝对定位</el-divider>
      <div class="slider-row">
        <span class="slider-label">Pan</span>
        <el-slider v-model="absPan" :min="-180" :max="180" :step="1" />
        <span class="slider-value">{{ absPan }}</span>
      </div>
      <div class="slider-row">
        <span class="slider-label">Tilt</span>
        <el-slider v-model="absTilt" :min="-90" :max="90" :step="1" />
        <span class="slider-value">{{ absTilt }}</span>
      </div>
      <div class="slider-row">
        <span class="slider-label">Zoom</span>
        <el-slider v-model="absZoom" :min="0" :max="1" :step="0.01" />
        <span class="slider-value">{{ absZoom.toFixed(2) }}</span>
      </div>
      <el-button type="primary" plain class="full-width" @click="gotoAbsolute">
        <el-icon><Position /></el-icon>转到
      </el-button>

      <el-divider content-position="left">预置位</el-divider>
      <div class="preset-input">
        <el-input v-model="presetName" placeholder="预置位名称" />
        <el-button type="primary" plain @click="savePreset">设为预置位</el-button>
      </div>
      <div v-if="state.presets.length" class="preset-list">
        <div v-for="p in state.presets" :key="p.token" class="preset-item">
          <span class="preset-name">{{ p.name || '未命名' }}</span>
          <span class="preset-token">#{{ p.token }}</span>
          <el-button size="small" @click="gotoPreset(p.token)">调用</el-button>
          <el-button size="small" type="danger" plain @click="removePreset(p.token)">删除</el-button>
        </div>
      </div>
      <el-empty v-else description="暂无预置位" :image-size="48" />

      <div class="home-row">
        <el-button @click="op({ op: 'home_set' }, '已设置 home')">
          <el-icon><HomeFilled /></el-icon>设为 home
        </el-button>
        <el-button @click="op({ op: 'home_goto' }, '已回到 home')">
          <el-icon><House /></el-icon>回到 home
        </el-button>
      </div>
    </div>
  </el-drawer>
</template>

<style scoped>
.ptz-body {
  min-height: 200px;
}
.dpad {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
  max-width: 204px;
  margin: 0 auto;
}
.dpad .el-button,
.dpad .el-button + .el-button {
  margin-left: 0;
}
.pad-btn {
  width: 100%;
  height: 44px;
  padding: 8px;
}
.zoom-row {
  display: flex;
  justify-content: center;
  gap: 8px;
  margin-top: 16px;
}
.zoom-row .el-button + .el-button {
  margin-left: 0;
}
.tip {
  margin: 12px 0 0;
  font-size: 12px;
  color: #909399;
  text-align: center;
}
.slider-row {
  display: flex;
  align-items: center;
  margin-bottom: 8px;
}
.slider-label {
  width: 40px;
  flex-shrink: 0;
  font-size: 13px;
  color: #606266;
}
.slider-row .el-slider {
  flex: 1;
  margin: 0 8px;
}
.slider-value {
  width: 48px;
  flex-shrink: 0;
  text-align: right;
  font-size: 12px;
  color: #909399;
}
.full-width {
  width: 100%;
}
.preset-input {
  display: flex;
  gap: 8px;
}
.preset-input .el-button {
  flex-shrink: 0;
}
.preset-list {
  margin-top: 12px;
}
.preset-item {
  display: flex;
  align-items: center;
  padding: 6px 0;
  border-bottom: 1px solid #ebeef5;
}
.preset-item .el-button + .el-button {
  margin-left: 8px;
}
.preset-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
  color: #303133;
}
.preset-token {
  margin-right: 12px;
  font-size: 12px;
  color: #909399;
}
.home-row {
  display: flex;
  justify-content: center;
  gap: 8px;
  margin-top: 16px;
}
.home-row .el-button + .el-button {
  margin-left: 0;
}
</style>
