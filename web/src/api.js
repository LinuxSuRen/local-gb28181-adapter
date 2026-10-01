// local-gb28181-adapter 管理台 HTTP 封装：统一处理 envelope、超时与友好错误提示
const TIMEOUT_MS = 15000

export class ApiError extends Error {
  constructor(message, code) {
    super(message)
    this.name = 'ApiError'
    this.code = code || 'unknown'
  }
}

async function request(path, { method = 'GET', body } = {}) {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), TIMEOUT_MS)
  let resp
  try {
    resp = await fetch(path, {
      method,
      headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
      signal: controller.signal
    })
  } catch (err) {
    if (err && err.name === 'AbortError') {
      throw new ApiError('请求超时，请检查网络后重试', 'timeout')
    }
    throw new ApiError('无法连接后端服务，请确认服务已启动', 'network')
  } finally {
    clearTimeout(timer)
  }

  if (!resp.ok) {
    let message = `请求失败（HTTP ${resp.status}）`
    let code = `http_${resp.status}`
    try {
      const payload = await resp.json()
      if (payload && payload.error) {
        if (payload.error.message) message = payload.error.message
        if (payload.error.code) code = payload.error.code
      }
    } catch (e) {
      // 响应体不是 JSON 时使用默认文案
    }
    throw new ApiError(message, code)
  }

  const text = await resp.text()
  if (!text) return null
  let payload = null
  try {
    payload = JSON.parse(text)
  } catch (e) {
    return null
  }
  if (payload && typeof payload === 'object' && Object.prototype.hasOwnProperty.call(payload, 'data')) {
    return payload.data
  }
  return payload
}

export function getSystem() {
  return request('/api/system')
}


export function getPlatform() {
  return request('/api/platform')
}

export function updatePlatform(platform) {
  return request('/api/platform', { method: 'PUT', body: platform })
}

export function getSessions() {
  return request('/api/sessions')
}

export function getCameras() {
  return request('/api/cameras')
}

export function createCamera(camera) {
  return request('/api/cameras', { method: 'POST', body: camera })
}

export function updateCamera(id, camera) {
  return request(`/api/cameras/${encodeURIComponent(id)}`, { method: 'PUT', body: camera })
}

export function deleteCamera(id) {
  return request(`/api/cameras/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function getPtz(id) {
  return request(`/api/cameras/${encodeURIComponent(id)}/ptz`)
}

export function sendPtz(id, operation) {
  return request(`/api/cameras/${encodeURIComponent(id)}/ptz`, { method: 'POST', body: operation })
}

export function snapshotUrl(id) {
  return `/api/cameras/${encodeURIComponent(id)}/snapshot?t=${Date.now()}`
}

export function listDevices() {
  return request('/api/devices')
}
