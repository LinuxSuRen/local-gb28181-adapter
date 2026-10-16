package uplevel

import (
	"sync"
	"time"

	"github.com/linuxsuren/local-gb28181-adapter/internal/manscdp"
)

// ChannelInfo 下级设备目录中的通道。
type ChannelInfo struct {
	DeviceID     string `json:"device_id"`
	Name         string `json:"name"`
	Status       string `json:"status"` // ON / OFF
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
}

// Device 已注册的下级国标设备。
type Device struct {
	DeviceID      string    `json:"device_id"`
	RemoteIP      string    `json:"remote_ip"`
	RemotePort    int       `json:"remote_port"`
	RegisteredAt  time.Time `json:"registered_at"`
	LastRegister  time.Time `json:"last_register"`
	LastKeepalive time.Time `json:"last_keepalive,omitempty"`
	Online        bool      `json:"online"`       // 注册在线
	KeepaliveOK   bool      `json:"keepalive_ok"` // 心跳是否存活
	Expires       int       `json:"expires"`      // 注册有效期（秒）

	// 目录与设备信息（查询后填充）。
	Channels []ChannelInfo               `json:"channels,omitempty"`
	Info     *manscdp.DeviceInfoResponse `json:"info,omitempty"`
}

// Registry 下级设备注册表：登记 REGISTER/心跳状态与目录缓存。
type Registry struct {
	mu      sync.Mutex
	devices map[string]*Device
}

// NewRegistry 创建注册表。
func NewRegistry() *Registry {
	return &Registry{devices: map[string]*Device{}}
}

// Register 登记一次成功注册（新增或刷新）。
func (r *Registry) Register(deviceID, ip string, port, expires int) *Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	d, ok := r.devices[deviceID]
	if !ok {
		d = &Device{DeviceID: deviceID}
		r.devices[deviceID] = d
		d.RegisteredAt = now
	}
	d.RemoteIP, d.RemotePort, d.Expires = ip, port, expires
	d.LastRegister = now
	d.Online = true
	return d
}

// Unregister 设备注销（Expires 0 或注册失败清理）。
func (r *Registry) Unregister(deviceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.devices[deviceID]; ok {
		d.Online = false
		d.KeepaliveOK = false
	}
}

// Keepalive 刷新心跳；设备未知时也登记（部分设备先心跳后注册可见）。
func (r *Registry) Keepalive(deviceID, ip string, port int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[deviceID]
	if !ok {
		d = &Device{DeviceID: deviceID, RegisteredAt: time.Now()}
		r.devices[deviceID] = d
	}
	d.RemoteIP, d.RemotePort = ip, port
	d.LastKeepalive = time.Now()
	d.KeepaliveOK = true
	d.Online = true
}

// UpdateChannels 更新设备目录缓存。
func (r *Registry) UpdateChannels(deviceID string, channels []ChannelInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.devices[deviceID]; ok {
		d.Channels = channels
	}
}

// UpdateInfo 更新设备信息缓存。
func (r *Registry) UpdateInfo(deviceID string, info *manscdp.DeviceInfoResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.devices[deviceID]; ok {
		d.Info = info
	}
}

// Snapshot 返回全部设备快照（深拷贝）。
func (r *Registry) Snapshot() []Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Device, 0, len(r.devices))
	for _, d := range r.devices {
		c := *d
		c.Channels = append([]ChannelInfo(nil), d.Channels...)
		if d.Info != nil {
			info := *d.Info
			c.Info = &info
		}
		out = append(out, c)
	}
	return out
}

// Find 按设备编码查找。
func (r *Registry) Find(deviceID string) (Device, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[deviceID]
	if !ok {
		return Device{}, false
	}
	c := *d
	c.Channels = append([]ChannelInfo(nil), d.Channels...)
	if d.Info != nil {
		info := *d.Info
		c.Info = &info
	}
	return c, true
}

// FindByChannel 按通道编码查找所属设备。
func (r *Registry) FindByChannel(channelID string) (Device, ChannelInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.devices {
		for _, ch := range d.Channels {
			if ch.DeviceID == channelID {
				dev := *d
				dev.Channels = append([]ChannelInfo(nil), d.Channels...)
				if d.Info != nil {
					info := *d.Info
					dev.Info = &info
				}
				return dev, ch, true
			}
		}
	}
	return Device{}, ChannelInfo{}, false
}

// SweepKeepalive 心跳超时巡检：超过 maxIdle 无心跳的设备标记离线。
func (r *Registry) SweepKeepalive(maxIdle time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.devices {
		if d.LastKeepalive.IsZero() {
			continue
		}
		if time.Since(d.LastKeepalive) > maxIdle {
			d.KeepaliveOK = false
		}
	}
}
