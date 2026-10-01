// Package ptzmock 提供虚拟云台状态机：不驱动任何真实硬件，
// 仅维护 pan/tilt/zoom 的虚拟位置并支持 ONVIF PTZ 全套语义。
package ptzmock

import (
	"strconv"
	"sync"
	"time"
)

// 坐标范围（绝对位置空间）。
const (
	PanMin  = -180.0
	PanMax  = 180.0
	TiltMin = -90.0
	TiltMax = 90.0
	ZoomMin = 0.0
	ZoomMax = 1.0
)

// Preset 虚拟预置位。
type Preset struct {
	Token string  `json:"token"`
	Name  string  `json:"name"`
	Pan   float64 `json:"pan"`
	Tilt  float64 `json:"tilt"`
	Zoom  float64 `json:"zoom"`
}

// Node 单个 profile 的虚拟云台节点。
type Node struct {
	mu sync.Mutex

	pan, tilt, zoom          float64
	panVel, tiltVel, zoomVel float64
	lastTick                 time.Time

	homePan, homeTilt, homeZoom float64
	presets                     []Preset
	nextPreset                  int
}

// NewNode 创建虚拟云台节点。
func NewNode() *Node {
	return &Node{lastTick: time.Now()}
}

// clamp 将值限制在 [min, max]。
func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// advance 按时间积分连续移动速度，越界后夹紧并停止该轴运动。
func (n *Node) advance() {
	now := time.Now()
	elapsed := now.Sub(n.lastTick)
	n.lastTick = now
	if elapsed <= 0 {
		return
	}
	if n.panVel == 0 && n.tiltVel == 0 && n.zoomVel == 0 {
		return
	}
	// 速度空间为 [-1,1]，映射为每秒最大行程：pan 90°/s、tilt 45°/s、zoom 0.5/s。
	n.pan = clamp(n.pan+n.panVel*90*elapsed.Seconds(), PanMin, PanMax)
	n.tilt = clamp(n.tilt+n.tiltVel*45*elapsed.Seconds(), TiltMin, TiltMax)
	n.zoom = clamp(n.zoom+n.zoomVel*0.5*elapsed.Seconds(), ZoomMin, ZoomMax)
	if n.pan == PanMin || n.pan == PanMax {
		n.panVel = 0
	}
	if n.tilt == TiltMin || n.tilt == TiltMax {
		n.tiltVel = 0
	}
	if n.zoom == ZoomMin || n.zoom == ZoomMax {
		n.zoomVel = 0
	}
}

// ContinuousMove 设置连续移动速度（范围 [-1,1]）。
func (n *Node) ContinuousMove(pan, tilt, zoom float64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.advance()
	if pan != 0 {
		n.panVel = clamp(pan, -1, 1)
	}
	if tilt != 0 {
		n.tiltVel = clamp(tilt, -1, 1)
	}
	if zoom != 0 {
		n.zoomVel = clamp(zoom, -1, 1)
	}
}

// Stop 停止连续移动。
func (n *Node) Stop(panTilt, zoom bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.advance()
	if panTilt {
		n.panVel, n.tiltVel = 0, 0
	}
	if zoom {
		n.zoomVel = 0
	}
}

// AbsoluteMove 直接设置绝对位置。
func (n *Node) AbsoluteMove(pan, tilt, zoom float64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.advance()
	n.panVel, n.tiltVel, n.zoomVel = 0, 0, 0
	// ONVIF 中 0 是合法坐标，三个分量全量生效。
	n.pan = clamp(pan, PanMin, PanMax)
	n.tilt = clamp(tilt, TiltMin, TiltMax)
	n.zoom = clamp(zoom, ZoomMin, ZoomMax)
}

// RelativeMove 在当前位置上叠加位移。
func (n *Node) RelativeMove(pan, tilt, zoom float64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.advance()
	n.panVel, n.tiltVel, n.zoomVel = 0, 0, 0
	n.pan = clamp(n.pan+pan, PanMin, PanMax)
	n.tilt = clamp(n.tilt+tilt, TiltMin, TiltMax)
	n.zoom = clamp(n.zoom+zoom, ZoomMin, ZoomMax)
}

// Status 返回当前位置与运动状态。
func (n *Node) Status() (pan, tilt, zoom float64, moving bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.advance()
	moving = n.panVel != 0 || n.tiltVel != 0 || n.zoomVel != 0
	return n.pan, n.tilt, n.zoom, moving
}

// SetHomePosition 将当前位置记为 home。
func (n *Node) SetHomePosition() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.advance()
	n.homePan, n.homeTilt, n.homeZoom = n.pan, n.tilt, n.zoom
}

// GotoHomePosition 回到 home 位。
func (n *Node) GotoHomePosition() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.advance()
	n.panVel, n.tiltVel, n.zoomVel = 0, 0, 0
	n.pan, n.tilt, n.zoom = n.homePan, n.homeTilt, n.homeZoom
}

// SetPreset 保存当前位姿为预置位，返回预置位 token。
func (n *Node) SetPreset(name string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.advance()
	token := strconv.Itoa(n.nextPreset)
	n.nextPreset++
	// 同名预置位覆盖（与多数相机行为一致）。
	for i := range n.presets {
		if n.presets[i].Name == name {
			n.presets[i] = Preset{Token: n.presets[i].Token, Name: name, Pan: n.pan, Tilt: n.tilt, Zoom: n.zoom}
			return n.presets[i].Token
		}
	}
	n.presets = append(n.presets, Preset{Token: token, Name: name, Pan: n.pan, Tilt: n.tilt, Zoom: n.zoom})
	return token
}

// Presets 返回全部预置位副本。
func (n *Node) Presets() []Preset {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.advance()
	return append([]Preset(nil), n.presets...)
}

// GotoPreset 转到指定预置位；token 不存在时返回 false。
func (n *Node) GotoPreset(token string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, p := range n.presets {
		if p.Token == token {
			n.advance()
			n.panVel, n.tiltVel, n.zoomVel = 0, 0, 0
			n.pan, n.tilt, n.zoom = p.Pan, p.Tilt, p.Zoom
			return true
		}
	}
	return false
}

// RemovePreset 删除指定预置位。
func (n *Node) RemovePreset(token string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := range n.presets {
		if n.presets[i].Token == token {
			n.presets = append(n.presets[:i], n.presets[i+1:]...)
			return true
		}
	}
	return false
}
