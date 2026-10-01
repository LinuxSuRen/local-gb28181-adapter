<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getPlatform, updatePlatform } from '../api'

const emit = defineEmits(['saved'])

const formRef = ref(null)
const saving = ref(false)
const passwordVisible = ref(false)

const form = reactive({
  enabled: false,
  server_id: '',
  server_addr: '',
  device_id: '',
  password: '',
  local_addr: ':5060',
  register_expires: 3600,
  keepalive_interval: 60
})

const rules = {
  server_id: [
    { pattern: /^\d{20}$/, message: '平台编码必须为 20 位数字', trigger: 'blur' }
  ],
  device_id: [
    { required: true, message: '设备编码不能为空', trigger: 'blur' },
    { pattern: /^\d{20}$/, message: '设备编码必须为 20 位数字', trigger: 'blur' }
  ],
  server_addr: [
    { required: true, message: '平台地址不能为空', trigger: 'blur' },
    { pattern: /^[\w.-]+:\d+$/, message: '格式应为 host:port', trigger: 'blur' }
  ]
}

onMounted(load)

async function load() {
  try {
    const p = await getPlatform()
    if (p) Object.assign(form, p)
  } catch (e) {
    ElMessage.error(e.message)
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
      server_addr: form.server_addr,
      device_id: form.device_id,
      local_addr: form.local_addr,
      register_expires: Number(form.register_expires),
      keepalive_interval: Number(form.keepalive_interval)
    }
    if (form.password) body.password = form.password
    await updatePlatform(body)
    ElMessage.success('平台配置已保存，信令服务已重载')
    form.password = ''
    emit('saved')
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span class="card-title">上级平台对接</span>
        <el-switch v-model="form.enabled" active-text="启用" inactive-text="停用" />
      </div>
    </template>
    <el-form ref="formRef" :model="form" :rules="rules" label-width="120px">
      <el-row :gutter="16">
        <el-col :span="12">
          <el-form-item label="平台编码" prop="server_id">
            <el-input v-model="form.server_id" placeholder="上级平台 SIP ID（20 位数字）" />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="平台地址" prop="server_addr">
            <el-input v-model="form.server_addr" placeholder="例如 192.168.1.10:5060" />
          </el-form-item>
        </el-col>
      </el-row>
      <el-row :gutter="16">
        <el-col :span="12">
          <el-form-item label="设备编码" prop="device_id">
            <el-input v-model="form.device_id" placeholder="本设备 SIP ID（20 位数字）" />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="注册密码">
            <el-input
              v-model="form.password"
              :type="passwordVisible ? 'text' : 'password'"
              :placeholder="form.server_addr && form.password !== undefined && form.password_set ? '已设置，留空保持不变' : '平台分配的注册密码'"
              autocomplete="new-password"
            >
              <template #suffix>
                <el-icon style="cursor: pointer" @click="passwordVisible = !passwordVisible">
                  <View v-if="passwordVisible" />
                  <Hide v-else />
                </el-icon>
              </template>
            </el-input>
          </el-form-item>
        </el-col>
      </el-row>
      <el-row :gutter="16">
        <el-col :span="8">
          <el-form-item label="本地 SIP 监听">
            <el-input v-model="form.local_addr" placeholder=":5060" />
          </el-form-item>
        </el-col>
        <el-col :span="8">
          <el-form-item label="注册有效期">
            <el-input-number v-model="form.register_expires" :min="60" :max="86400" :step="60" />
            <span class="unit">秒</span>
          </el-form-item>
        </el-col>
        <el-col :span="8">
          <el-form-item label="心跳间隔">
            <el-input-number v-model="form.keepalive_interval" :min="5" :max="3600" :step="5" />
            <span class="unit">秒</span>
          </el-form-item>
        </el-col>
      </el-row>
      <el-form-item>
        <el-button type="primary" :loading="saving" @click="save">保存并重载</el-button>
        <span class="hint">保存后立即重新注册；密码留空表示不修改</span>
      </el-form-item>
    </el-form>
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
.unit {
  margin-left: 6px;
  color: #909399;
  font-size: 12px;
}
.hint {
  margin-left: 12px;
  color: #909399;
  font-size: 12px;
}
</style>
