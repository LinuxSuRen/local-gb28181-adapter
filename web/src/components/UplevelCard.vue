<script setup>
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  getUplevel, updateUplevel, getUplevelDevices, getUplevelSessions,
  queryUplevelDevice, playUplevelChannel, stopUplevelChannel, sendUplevelPtz,
  uplevelSnapshotUrl
} from '../api'

const emit = defineEmits(['saved'])

const formRef = ref(null)
const saving = ref(false)
const devices = ref([])
const sessions = ref([])
const playing = ref({}) // channelID → session 信息
const querying = ref('')
let pollTimer = null

const form = reactive({
  enabled: false,
  server_id: '',
  local_addr: ':5060',
  password: '',
  media_port_min: 30000,
  media_port_max: 30499
})

const rules = {
  server_id: [
    { pattern: /^\d{20}$/, message: '平台编码必须为 20 位数字', trigger: 'blur' }
  ],
  local_addr: [
    { pattern: /^([\w.-]*:\d+|:\d+)$/, message: '格式应为 :5060 或 host:port', trigger: 'blur' }
  ]
}

// 目录里的全部通道（展开为带设备信息的行）
const channels = computed(() => {
  const out = []
  for (const dev of devices.value) {
    for (const ch of dev.channels || []) {
      out.push({
        ...ch,
        device_id: dev.device_id,
        device_online: dev.online,
        keepalive_ok: dev.keepalive_ok,
        parent_name: dev.info?.device_name || dev.device_id
      })
    }
  }
  return out
})

onMounted(() => {
  load()
  pollTimer = setInterval(loadDevices, 5000)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})

async function load() {
  try {
    const u = await getUplevel()
    if (u) {
      form.enabled = !!u.enabled
      form.server_id = u.server_id || ''
      form.local_addr = u.local_addr || ':5060'
      form.media_port_min = u.media_port_min || 30000
      form.media_port_max = u.media_port_max || 30499
    }
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function loadDevices() {
  try {
    const [devs, sss] = await Promise.all([getUplevelDevices(), getUplevelSessions()])
    devices.value = Array.isArray(devs) ? devs : []
    sessions.value = Array.isArray(sss) ? sss : []
    const map = {}
    for (const s of sessions.value) map[s.channel_id] = s
    playing.value = map
  } catch (e) {
    // 服务未启动时静默（避免轮询报错刷屏）
  }
}

async function save() {
  try {
    await formRef.value.validate()
  } catch (e) {
    return
  }
  saving.value = true
  try {
    const body = {
      enabled: form.enabled,
      server_id: form.server_id,
      local_addr: form.local_addr,
      media_port_min: Number(form.media_port_min),
      media_port_max: Number(form.media_port_max)
    }
    if (form.password) body.password = form.password
    await updateUplevel(body)
    ElMessage.success('国标平台配置已保存')
    form.password = ''
    emit('saved')
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    saving.value = false
  }
}

async function refreshDevice(dev) {
  querying.value = dev.device_id
  try {
    await queryUplevelDevice(dev.device_id)
    ElMessage.success('目录已刷新')
    await loadDevices()
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    querying.value = ''
  }
}

async function togglePlay(ch) {
  if (playing.value[ch.device_id]) {
    try {
      await stopUplevelChannel(ch.device_id)
      ElMessage.success('已停止点播')
    } catch (e) {
      ElMessage.error(e.message)
    }
  } else {
    try {
      const r = await playUplevelChannel(ch.device_id)
      ElMessage.success('点播成功，可抓图预览或用 RTSP 地址播放')
      if (r?.rtsp_url) {
        try {
          await navigator.clipboard.writeText(r.rtsp_url)
          ElMessage.info(`RTSP 地址已复制：${r.rtsp_url}`)
        } catch (e) { /* 剪贴板不可用忽略 */ }
      }
    } catch (e) {
      ElMessage.error(e.message)
    }
  }
  await loadDevices()
}

async function ptz(channelId, action) {
  try {
    await sendUplevelPtz(channelId, action, 80)
  } catch (e) {
    ElMessage.error(e.message)
  }
}

// 抓图预览
const snapVisible = ref(false)
const snapChannel = ref(null)
const snapUrl = ref('')

function openSnapshot(ch) {
  snapChannel.value = ch
  snapVisible.value = true
  snapUrl.value = uplevelSnapshotUrl(ch.device_id)
}

function refreshSnapshot() {
  snapUrl.value = uplevelSnapshotUrl(snapChannel.value.device_id)
}

function onSnapError() {
  ElMessage.warning('画面抓取失败，请确认点播会话仍在进行')
  snapVisible.value = false
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span class="card-title">国标下级设备接入（上级平台模式）</span>
        <el-switch v-model="form.enabled" active-text="启用" inactive-text="停用" />
      </div>
    </template>

    <el-form ref="formRef" :model="form" :rules="rules" label-width="120px">
      <el-row :gutter="16">
        <el-col :span="8">
          <el-form-item label="平台编码" prop="server_id">
            <el-input v-model="form.server_id" placeholder="本平台 SIP ID（20 位数字）" />
          </el-form-item>
        </el-col>
        <el-col :span="8">
          <el-form-item label="SIP 监听" prop="local_addr">
            <el-input v-model="form.local_addr" placeholder=":5060" />
          </el-form-item>
        </el-col>
        <el-col :span="8">
          <el-form-item label="注册密码">
            <el-input v-model="form.password" type="password" show-password
              placeholder="下级设备接入密码（留空不变更）" />
          </el-form-item>
        </el-col>
      </el-row>
      <el-row :gutter="16">
        <el-col :span="8">
          <el-form-item label="收流端口下限">
            <el-input-number v-model="form.media_port_min" :min="1024" :max="65000" />
          </el-form-item>
        </el-col>
        <el-col :span="8">
          <el-form-item label="收流端口上限">
            <el-input-number v-model="form.media_port_max" :min="1024" :max="65000" />
          </el-form-item>
        </el-col>
        <el-col :span="8">
          <el-form-item>
            <el-button type="primary" :loading="saving" @click="save">保存配置</el-button>
          </el-form-item>
        </el-col>
      </el-row>
    </el-form>

    <!-- 已注册设备 -->
    <el-divider content-position="left">已注册设备</el-divider>
    <el-empty v-if="devices.length === 0" description="暂无国标设备注册。在相机的 GB28181（平台接入）配置中把 SIP 服务器指向本机地址与端口即可。" :image-size="60" />
    <el-table v-else :data="devices" size="small">
      <el-table-column prop="device_id" label="设备编码" min-width="180" />
      <el-table-column label="状态" width="120">
        <template #default="{ row }">
          <el-tag :type="row.online ? 'success' : 'danger'" size="small">
            {{ row.online ? '已注册' : '离线' }}
          </el-tag>
          <el-tag v-if="row.keepalive_ok" type="success" effect="plain" size="small" style="margin-left: 4px">
            心跳
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="设备信息" min-width="180">
        <template #default="{ row }">
          <span v-if="row.info">{{ row.info.manufacturer }} {{ row.info.model }}</span>
          <span v-else class="muted">未查询</span>
        </template>
      </el-table-column>
      <el-table-column prop="remote_ip" label="地址" width="150" />
      <el-table-column label="通道数" width="80">
        <template #default="{ row }">{{ (row.channels || []).length }}</template>
      </el-table-column>
      <el-table-column label="操作" width="120">
        <template #default="{ row }">
          <el-button size="small" type="primary" plain :loading="querying === row.device_id"
            @click="refreshDevice(row)">查询目录</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 通道 -->
    <template v-if="channels.length > 0">
      <el-divider content-position="left">通道（点播测试）</el-divider>
      <el-table :data="channels" size="small">
        <el-table-column prop="device_id" label="通道编码" min-width="180" />
        <el-table-column prop="name" label="名称" min-width="160" show-overflow-tooltip />
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag :type="row.status === 'ON' ? 'success' : 'info'" size="small">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="点播" width="100">
          <template #default="{ row }">
            <el-button v-if="!playing[row.device_id]" size="small" type="success" plain
              @click="togglePlay(row)">点播</el-button>
            <el-button v-else size="small" type="danger" plain
              @click="togglePlay(row)">停止</el-button>
          </template>
        </el-table-column>
        <el-table-column label="画面" width="100">
          <template #default="{ row }">
            <el-button size="small" plain :disabled="!playing[row.device_id]"
              @click="openSnapshot(row)">抓图</el-button>
          </template>
        </el-table-column>
        <el-table-column label="云台" min-width="240">
          <template #default="{ row }">
            <el-button-group v-if="playing[row.device_id] || true">
              <el-button size="small" @mousedown="ptz(row.device_id, 'left')" @mouseup="ptz(row.device_id, 'stop')">←</el-button>
              <el-button size="small" @mousedown="ptz(row.device_id, 'up')" @mouseup="ptz(row.device_id, 'stop')">↑</el-button>
              <el-button size="small" @mousedown="ptz(row.device_id, 'down')" @mouseup="ptz(row.device_id, 'stop')">↓</el-button>
              <el-button size="small" @mousedown="ptz(row.device_id, 'right')" @mouseup="ptz(row.device_id, 'stop')">→</el-button>
              <el-button size="small" @mousedown="ptz(row.device_id, 'zoom_in')" @mouseup="ptz(row.device_id, 'stop')">放大</el-button>
              <el-button size="small" @mousedown="ptz(row.device_id, 'zoom_out')" @mouseup="ptz(row.device_id, 'stop')">缩小</el-button>
            </el-button-group>
          </template>
        </el-table-column>
      </el-table>
    </template>

    <!-- 抓图预览 -->
    <el-dialog v-model="snapVisible" :title="`通道画面 - ${snapChannel?.name || snapChannel?.device_id || ''}`" width="720px">
      <div v-if="snapUrl" class="snap-box">
        <img :src="snapUrl" alt="通道画面" @error="onSnapError" />
      </div>
      <template #footer>
        <el-button @click="refreshSnapshot">刷新</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>

<style scoped>
.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.card-title {
  font-weight: 600;
}
.muted {
  color: #909399;
}
.snap-box {
  text-align: center;
  min-height: 240px;
}
.snap-box img {
  max-width: 100%;
}
</style>
