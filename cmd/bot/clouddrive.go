// 云盘下载（/download 指令）状态适配器：组合配置快照与 rclone 探测，
// 实现 botapi.CloudStatus（/download 准入预检）。
package main

import (
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/cloudarchive"
)

// cloudRecheckInterval 是 rclone 可用性周期复查间隔：启动探测 + 低频复查
// （如 10 分钟）。
const cloudRecheckInterval = 10 * time.Minute

// cloudDriveStatus 组合 cloudarchive.Manager 配置快照与 rclone 探测，实现
// botapi.CloudStatus（/download 准入预检）。Available 每次实时探测（BinPath
// 只是 stat/LookPath，/download 提交频度下开销可忽略）；其余走 Manager 的
// 无锁内存快照。凭据与 options 值不出现在任何返回值中。
type cloudDriveStatus struct {
	mgr *cloudarchive.Manager
}

func (s cloudDriveStatus) Enabled() bool { return s.mgr.Enabled() }

func (s cloudDriveStatus) Available() bool {
	_, err := cloudarchive.BinPath()
	return err == nil
}

func (s cloudDriveStatus) DefaultDestination() string {
	return s.mgr.Snapshot().DefaultDestination
}

func (s cloudDriveStatus) DestinationEnabled(name string) bool {
	_, ok := s.mgr.ResolveCloudDestination(name)
	return ok
}

func (s cloudDriveStatus) EnabledDestinations() []string {
	var names []string
	for _, d := range s.mgr.Snapshot().Destinations {
		if d.Enabled {
			names = append(names, d.Name)
		}
	}
	return names
}
