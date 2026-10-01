<script setup>
import { reactive } from 'vue'
import { ElMessage } from 'element-plus'
import { updateCamera } from '../api'

defineProps({
  cameras: { type: Array, default: () => [] },
  loading: { type: Boolean, default: false },
  showChannel: { type: Boolean, default: false }
})

const emit = defineEmits(['add', 'refresh', 'edit', 'snapshot', 'ptz', 'remove'])

const TYPE_META = {
  v4l2: { label: '本地摄像头', tag: 'primary' },
  avfoundation: { label: '本地摄像头', tag: 'primary' },
  dshow: { label: '本地摄像头', tag: 'primary' },
  rtsp: { label: 'RTSP 拉流', tag: 'warning' },
  testsrc: { label: '测试彩条', tag: 'info' }
}

const toggling = reactive({})

function typeLabel(type) {
  return (TYPE_META[type] && TYPE_META[type].label) || type
}

function typeTag(type) {
  return (TYPE_META[type] && TYPE_META[type].tag) || 'info'
}

function isRunning(row) {
  return !!(row.status && row.status.running)
}

function lastError(row) {
  return (row.status && row.status.last_error) || ''
}

function formatResolution(row) {
  const size = row.width ? `${row.width}×${row.height}` : '自动'
  return `${size}@${row.framerate}`
}

function cameraBody(row, overrides = {}) {
  return {
    name: row.name,
    type: row.type,
    source: row.source,
    width: row.width,
    height: row.height,
    framerate: row.framerate,
    bitrate_kbps: row.bitrate_kbps,
    infrared: row.infrared,
    enabled: row.enabled,
    channel_id: row.channel_id,
    ...overrides
  }
}

function channelID(row) {
  if (!row.enabled) return row.channel_id || ''
  return (row.status && row.status.channel_id) || row.channel_id || ''
}

async function onToggleEnabled(row, value) {
  toggling[row.id] = true
  try {
    const updated = await updateCamera(row.id, cameraBody(row, { enabled: value }))
    Object.assign(row, updated || {})
    ElMessage.success('已保存')
  } catch (e) {
    // 失败时不改 row.enabled，开关自动回滚到旧值
    ElMessage.error(e.message)
  } finally {
    delete toggling[row.id]
  }
}

function rtspURL(row) {
  return (row.status && row.status.restream_url) || ''
}

async function copyRTSP(row) {
  const url = rtspURL(row)
  if (!url) {
    ElMessage.warning('摄像头未启用，暂无 RTSP 地址')
    return
  }
  try {
    await navigator.clipboard.writeText(url)
    ElMessage.success(`已复制 ${url}`)
  } catch {
    ElMessage.error('复制失败，请手动复制：' + url)
  }
}
</script>

<template>
  <el-card shadow="never">
    <div class="toolbar">
      <span class="card-title">摄像头列表</span>
      <div>
        <el-button type="primary" @click="emit('add')">
          <el-icon><Plus /></el-icon>添加摄像头
        </el-button>
        <el-button :loading="loading" @click="emit('refresh')">
          <el-icon><Refresh /></el-icon>刷新
        </el-button>
      </div>
    </div>

    <el-table :data="cameras" v-loading="loading">
      <el-table-column label="名称" min-width="180">
        <template #default="{ row }">
          <span>{{ row.name }}</span>
          <el-tag v-if="row.infrared" size="small" type="warning" class="name-tag">红外</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="类型" width="130">
        <template #default="{ row }">
          <el-tag size="small" :type="typeTag(row.type)">{{ typeLabel(row.type) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column v-if="showChannel" label="国标通道编码" min-width="190">
        <template #default="{ row }">
          <span class="channel-id">{{ channelID(row) || '—' }}</span>
        </template>
      </el-table-column>
      <el-table-column label="源" min-width="180" show-overflow-tooltip>
        <template #default="{ row }">
          <span>{{ row.source || '—' }}</span>
        </template>
      </el-table-column>
      <el-table-column label="分辨率/帧率" width="140">
        <template #default="{ row }">
          <span>{{ formatResolution(row) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="启用" width="80" align="center">
        <template #default="{ row }">
          <el-switch
            :model-value="row.enabled"
            :loading="!!toggling[row.id]"
            @change="(v) => onToggleEnabled(row, v)"
          />
        </template>
      </el-table-column>
      <el-table-column label="运行状态" width="110">
        <template #default="{ row }">
          <el-tooltip v-if="lastError(row)" :content="lastError(row)" placement="top">
            <span class="run-status">
              <span class="dot" :class="isRunning(row) ? 'dot-running' : 'dot-stopped'"></span>
              {{ isRunning(row) ? '运行中' : '已停止' }}
            </span>
          </el-tooltip>
          <span v-else class="run-status">
            <span class="dot" :class="isRunning(row) ? 'dot-running' : 'dot-stopped'"></span>
            {{ isRunning(row) ? '运行中' : '已停止' }}
          </span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="320" fixed="right">
        <template #default="{ row }">
          <el-tooltip
            :content="rtspURL(row) || '摄像头未启用，暂无 RTSP 地址'"
            placement="top"
          >
            <el-button link type="primary" :disabled="!rtspURL(row)" @click="copyRTSP(row)">
              <el-icon><Link /></el-icon>RTSP
            </el-button>
          </el-tooltip>
          <el-button link type="primary" @click="emit('snapshot', row)">
            <el-icon><Camera /></el-icon>快照
          </el-button>
          <el-button link type="primary" @click="emit('ptz', row)">
            <el-icon><Position /></el-icon>PTZ 试控
          </el-button>
          <el-button link type="primary" @click="emit('edit', row)">
            <el-icon><Edit /></el-icon>编辑
          </el-button>
          <el-popconfirm
            title="确定删除该摄像头？"
            confirm-button-text="确定"
            cancel-button-text="取消"
            @confirm="emit('remove', row)"
          >
            <template #reference>
              <el-button link type="danger">
                <el-icon><Delete /></el-icon>删除
              </el-button>
            </template>
          </el-popconfirm>
        </template>
      </el-table-column>
      <template #empty>
        <el-empty description="还没有摄像头，点击上方按钮添加" />
      </template>
    </el-table>
  </el-card>
</template>

<style scoped>
.toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}
.card-title {
  font-weight: 600;
}
.name-tag {
  margin-left: 8px;
}
.run-status {
  display: inline-flex;
  align-items: center;
}
.dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 6px;
}
.dot-running {
  background: #67c23a;
}
.dot-stopped {
  background: #c0c4cc;
}
.channel-id {
  font-family: monospace;
  font-size: 12px;
}
</style>
