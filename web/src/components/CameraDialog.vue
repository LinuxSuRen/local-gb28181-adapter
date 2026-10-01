<script setup>
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { createCamera, updateCamera, listDevices } from '../api'

const props = defineProps({
  visible: { type: Boolean, default: false },
  camera: { type: Object, default: null }
})
const emit = defineEmits(['update:visible', 'saved'])

const show = computed({
  get: () => props.visible,
  set: (v) => emit('update:visible', v)
})

const formRef = ref()
const saving = ref(false)

const DEFAULT_FORM = {
  name: '',
  type: '',
  source: '',
  width: 0,
  height: 0,
  framerate: 15,
  bitrate_kbps: 2048,
  infrared: false,
  enabled: true,
  channel_id: ''
}

const form = reactive({ ...DEFAULT_FORM })

// 用户只选择"来源方式"，平台差异（v4l2/avfoundation/dshow）由设备枚举结果自动携带。
const LOCAL_TYPES = ['v4l2', 'avfoundation', 'dshow']
const MODE_OPTIONS = [
  { value: 'local', label: '本地摄像头' },
  { value: 'rtsp', label: '网络摄像头（RTSP）' },
  { value: 'testsrc', label: '测试彩条' }
]
const mode = ref('local')

// 本地设备枚举。
const devices = ref([])
const devicesLoading = ref(false)
const devicesEnumFailed = ref(false)
const defaultLocalType = ref('')
let devicesLoaded = false

async function loadDevices(showToast = false) {
  devicesLoading.value = true
  try {
    const result = await listDevices()
    devices.value = Array.isArray(result && result.devices) ? result.devices : []
    defaultLocalType.value = (result && result.default_type) || ''
    devicesEnumFailed.value = false
  } catch (e) {
    devices.value = []
    devicesEnumFailed.value = true
    if (showToast) ElMessage.warning(e.message)
  } finally {
    devicesLoading.value = false
    devicesLoaded = true
  }
}

watch(
  () => [props.visible, mode.value],
  ([visible, m]) => {
    if (visible && m === 'local' && !devicesLoaded) {
      loadDevices()
    }
  }
)

function kindLabel(kind) {
  if (kind === 'builtin') return '内置'
  if (kind === 'usb') return 'USB'
  return ''
}

function deviceLabel(d) {
  const k = kindLabel(d.kind)
  return k ? `${d.name}（${k}）` : d.name
}

// 选择本地设备：类型随设备携带。
function onDeviceChange(source) {
  const dev = devices.value.find((d) => d.source === source)
  form.type = dev ? dev.type : defaultLocalType.value
}

watch(mode, (m) => {
  if (m === 'rtsp') form.type = 'rtsp'
  if (m === 'testsrc') form.type = 'testsrc'
  if (m === 'local' && !form.type) form.type = defaultLocalType.value
  nextTick(() => formRef.value && formRef.value.clearValidate('source'))
})

const SOURCE_PLACEHOLDER = {
  rtsp: 'rtsp://user:pass@ip:554/stream',
  testsrc: '留空即可，生成 1280×720 彩条'
}

const rules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  source: [
    {
      validator: (rule, value, callback) => {
        if (mode.value === 'testsrc') return callback()
        if (!value || !String(value).trim()) {
          return callback(new Error(mode.value === 'local' ? '请选择摄像头设备' : '请输入源'))
        }
        if (mode.value === 'rtsp' && !String(value).trim().startsWith('rtsp://')) {
          return callback(new Error('RTSP 源必须以 rtsp:// 开头'))
        }
        callback()
      },
      trigger: ['blur', 'change']
    }
  ]
}

watch(
  () => props.visible,
  (v) => {
    if (!v) return
    const c = props.camera
    if (c) {
      Object.assign(form, DEFAULT_FORM, {
        name: c.name != null ? c.name : '',
        type: c.type != null ? c.type : '',
        source: c.source != null ? c.source : '',
        width: c.width != null ? c.width : 0,
        height: c.height != null ? c.height : 0,
        framerate: c.framerate != null ? c.framerate : 15,
        bitrate_kbps: c.bitrate_kbps != null ? c.bitrate_kbps : 2048,
        infrared: !!c.infrared,
        enabled: !!c.enabled,
        channel_id: c.channel_id != null ? c.channel_id : ''
      })
      mode.value = LOCAL_TYPES.includes(c.type) ? 'local' : c.type || 'local'
    } else {
      Object.assign(form, DEFAULT_FORM)
      mode.value = 'local'
      form.type = defaultLocalType.value
    }
    nextTick(() => formRef.value && formRef.value.clearValidate())
  }
)

async function handleSave() {
  try {
    await formRef.value.validate()
  } catch (e) {
    return
  }
  // 兜底：本地模式下类型始终跟随设备或平台默认值。
  let type = form.type
  if (mode.value === 'local') {
    const dev = devices.value.find((d) => d.source === form.source)
    type = dev ? dev.type : defaultLocalType.value
  } else {
    type = mode.value
  }
  saving.value = true
  const body = {
    name: form.name.trim(),
    type,
    source: mode.value === 'testsrc' ? '' : form.source.trim(),
    width: form.width,
    height: form.height,
    framerate: form.framerate,
    bitrate_kbps: form.bitrate_kbps,
    infrared: form.infrared,
    enabled: form.enabled,
    channel_id: (form.channel_id || '').trim()
  }
  try {
    if (props.camera && props.camera.id) {
      await updateCamera(props.camera.id, body)
    } else {
      await createCamera(body)
    }
    ElMessage.success('已保存')
    emit('saved')
    show.value = false
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <el-dialog v-model="show" :title="camera ? '编辑摄像头' : '添加摄像头'" width="560px">
    <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
      <el-form-item label="名称" prop="name">
        <el-input v-model="form.name" placeholder="请输入名称" />
      </el-form-item>
      <el-form-item label="来源方式">
        <el-radio-group v-model="mode">
          <el-radio-button v-for="m in MODE_OPTIONS" :key="m.value" :value="m.value">
            {{ m.label }}
          </el-radio-button>
        </el-radio-group>
      </el-form-item>
      <el-form-item v-if="mode === 'local'" label="摄像头" prop="source">
        <div class="num-row">
          <el-select
            v-model="form.source"
            class="full-width"
            filterable
            allow-create
            default-first-option
            :loading="devicesLoading"
            placeholder="选择检测到的摄像头，或直接输入设备源"
            @change="onDeviceChange"
          >
            <el-option v-for="d in devices" :key="d.source" :label="deviceLabel(d)" :value="d.source" />
          </el-select>
          <el-button class="enum-refresh" :loading="devicesLoading" @click="loadDevices(true)">刷新</el-button>
        </div>
        <div class="form-tip">
          {{ devicesEnumFailed
            ? '设备枚举不可用，请确认 ffmpeg 已安装；也可手动输入设备源'
            : devices.length === 0 && !devicesLoading
              ? '未检测到本地摄像头，接入后点刷新；也可手动输入设备源'
              : '列表为当前主机检测到的内置与 USB 摄像头' }}
        </div>
      </el-form-item>
      <el-form-item v-else label="源" prop="source">
        <el-input
          v-model="form.source"
          :disabled="mode === 'testsrc'"
          :placeholder="SOURCE_PLACEHOLDER[mode]"
        />
      </el-form-item>
      <el-form-item label="分辨率">
        <div class="num-row">
          <el-input-number v-model="form.width" :min="0" :max="3840" controls-position="right" placeholder="宽" />
          <span class="num-sep">×</span>
          <el-input-number v-model="form.height" :min="0" :max="3840" controls-position="right" placeholder="高" />
        </div>
        <div class="form-tip">宽高为 0 表示自动</div>
      </el-form-item>
      <el-form-item label="帧率">
        <el-input-number v-model="form.framerate" :min="1" :max="60" />
      </el-form-item>
      <el-form-item label="码率">
        <div class="num-row">
          <el-input-number v-model="form.bitrate_kbps" :min="256" :max="8192" />
          <span class="num-sep">kbps</span>
        </div>
      </el-form-item>
      <el-form-item label="红外">
        <el-switch v-model="form.infrared" />
        <div class="form-tip">标记为红外通道后，目录中的通道名会带 infrared，平台可按名称识别红外</div>
      </el-form-item>
      <el-form-item label="通道编码">
        <el-input v-model="form.channel_id" placeholder="留空按设备编码自动派生" />
        <div class="form-tip">国标 20 位数字；留空时按设备编码前缀与摄像头顺序自动生成</div>
      </el-form-item>
      <el-form-item label="启用">
        <el-switch v-model="form.enabled" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="show = false">取消</el-button>
      <el-button type="primary" :loading="saving" @click="handleSave">确定</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.full-width {
  width: 100%;
}
.num-row {
  display: flex;
  align-items: center;
  width: 100%;
}
.num-row .el-input-number {
  flex: 1;
}
.num-sep {
  margin: 0 8px;
  color: #909399;
}
.form-tip {
  width: 100%;
  font-size: 12px;
  color: #909399;
  line-height: 1.5;
  margin-top: 4px;
}
.enum-refresh {
  margin-left: 8px;
}
</style>
