<script setup>
const props = defineProps({
  system: { type: Object, default: null },
  sessions: { type: Array, default: () => [] }
})

function stateTag(state) {
  switch (state) {
    case 'registered':
      return 'success'
    case 'registering':
      return 'warning'
    case 'unregistered':
      return 'danger'
    default:
      return 'info'
  }
}

function stateText(state) {
  switch (state) {
    case 'registered':
      return '在线'
    case 'registering':
      return '注册中'
    case 'unregistered':
      return '离线'
    case 'idle':
      return '未启用'
    default:
      return '未知'
  }
}

function formatTime(t) {
  if (!t) return '—'
  return new Date(t).toLocaleString()
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <span class="card-title">系统信息</span>
    </template>
    <el-descriptions :column="2" border>
      <el-descriptions-item label="设备编码">
        {{ system && system.device_id ? system.device_id : '—' }}
      </el-descriptions-item>
      <el-descriptions-item label="注册状态">
        <template v-if="system && system.registration">
          <el-tag :type="stateTag(system.registration.state)" size="small">
            {{ stateText(system.registration.state) }}
          </el-tag>
          <span v-if="system.registration.keepalive_fails > 0" class="keep-fail">
            心跳失败 {{ system.registration.keepalive_fails }} 次
          </span>
        </template>
        <span v-else>—</span>
      </el-descriptions-item>
      <el-descriptions-item label="最近注册">
        {{ system && system.registration ? formatTime(system.registration.last_register_at) : '—' }}
      </el-descriptions-item>
      <el-descriptions-item label="最近心跳">
        {{ system && system.registration ? formatTime(system.registration.last_keepalive) : '—' }}
      </el-descriptions-item>
      <el-descriptions-item label="国标通道数">
        {{ system && system.channel_count != null ? system.channel_count : '—' }}
      </el-descriptions-item>
      <el-descriptions-item label="正在点播">
        {{ sessions.length }} 路
      </el-descriptions-item>
      <el-descriptions-item label="RTSP 端口">
        {{ system && system.rtsp_port != null ? system.rtsp_port : '—' }}
      </el-descriptions-item>
      <el-descriptions-item label="版本">
        {{ system && system.version ? system.version : '—' }}
      </el-descriptions-item>
    </el-descriptions>

    <div v-if="sessions.length" class="sessions">
      <el-table :data="sessions" size="small" border>
        <el-table-column prop="channel_id" label="通道编码" min-width="180" />
        <el-table-column prop="camera_id" label="摄像头" width="90" />
        <el-table-column prop="transport" label="传输" width="80" />
        <el-table-column prop="remote" label="平台收流地址" min-width="160" />
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag :type="row.confirmed ? 'success' : 'warning'" size="small">
              {{ row.confirmed ? '推流中' : '等待确认' }}
            </el-tag>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </el-card>
</template>

<style scoped>
.card-title {
  font-weight: 600;
}
.keep-fail {
  margin-left: 8px;
  color: #e6a23c;
  font-size: 12px;
}
.sessions {
  margin-top: 12px;
}
</style>
