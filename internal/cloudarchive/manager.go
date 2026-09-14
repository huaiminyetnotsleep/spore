package cloudarchive

import (
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
)

// Manager 持有云盘配置的内存快照（atomic.Pointer，读取无锁）。
// 启动时加载；web 管理台保存经 Save 全量校验后替换快照；
// 外部修改文件后可经 Load 重读。凭据值只存在于快照与 0600 配置文件，
// 任何日志与错误信息不得携带。
type Manager struct {
	path string
	ptr  atomic.Pointer[Config]
	log  *slog.Logger
	mu   sync.Mutex
}

// NewManager 创建管理器并从 path 加载初始快照。加载失败（文件损坏等）
// 记 Warn 并保持零值快照（功能关闭态），不阻断进程启动——调用方可另行
// 上报 cloud.config_invalid 事件（装配层职责）。
func NewManager(path string, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{path: path, log: log}
	m.ptr.Store(&Config{})
	if err := m.Load(); err != nil {
		log.Warn("云盘配置加载失败，功能按未配置处理", "error", err.Error())
	}
	return m
}

// Load 重读磁盘配置并替换快照。读取或校验失败时保持旧快照并返回错误
// （调用方据此上报 cloud.config_invalid）；文件不存在是合法的零值态。
func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked()
}

func (m *Manager) loadLocked() error {
	cfg, err := LoadFile(m.path)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	cfg = cloneConfig(cfg)
	m.ptr.Store(&cfg)
	return nil
}

// Save 校验 cfg、原子写入磁盘并替换内存快照。
// Save、Restore 与 Rollback 共享写锁，失败时不改变文件或已发布快照。
func (m *Manager) Save(cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked(cfg)
}

func (m *Manager) saveLocked(cfg Config) error {
	cfg = cloneConfig(cfg)
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := SaveFile(m.path, cfg); err != nil {
		return err
	}
	m.ptr.Store(&cfg)
	return nil
}

// Restore 整体替换当前配置并在线发布新快照。替换前把当前快照写入固定的
// rollback-latest 文件；候选写入失败时当前文件与快照保持不变。
func (m *Manager) Restore(cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg = cloneConfig(cfg)
	if err := cfg.Validate(); err != nil {
		return err
	}
	current := cloneConfig(m.snapshotLocked())
	if err := SaveFile(m.RollbackPath(), current); err != nil {
		return err
	}
	if err := SaveFile(m.path, cfg); err != nil {
		return err
	}
	m.ptr.Store(&cfg)
	return nil
}

// Rollback 恢复最近一次 Restore 前保存的配置，并立即发布内存快照。
func (m *Manager) Rollback() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := os.Stat(m.RollbackPath()); errors.Is(err, os.ErrNotExist) {
		return errors.New("没有可用的云盘配置回滚点")
	} else if err != nil {
		return err
	}
	cfg, err := LoadFile(m.RollbackPath())
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := m.saveLocked(cfg); err != nil {
		return err
	}
	if err := os.Remove(m.RollbackPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.log.Warn("清理已使用的云盘配置回滚点失败", "error", err.Error())
	}
	return nil
}

// HasRollback 报告最近一次恢复前配置是否存在。
func (m *Manager) HasRollback() bool {
	fi, err := os.Stat(m.RollbackPath())
	return err == nil && fi.Mode().IsRegular()
}

// RollbackPath 返回固定的最近一次回滚文件路径。
func (m *Manager) RollbackPath() string { return m.path + ".rollback-latest" }

func (m *Manager) snapshotLocked() Config {
	if c := m.ptr.Load(); c != nil {
		return *c
	}
	return Config{}
}

func cloneConfig(cfg Config) Config {
	out := cfg
	out.Destinations = make([]Destination, len(cfg.Destinations))
	for i, d := range cfg.Destinations {
		out.Destinations[i] = d
		out.Destinations[i].Options = make(map[string]string, len(d.Options))
		for key, value := range d.Options {
			out.Destinations[i].Options[key] = value
		}
	}
	return out
}

// Snapshot 返回当前配置的深拷贝视图，调用方修改切片或 options map 不会
// 影响已发布快照；配置替换仍始终整体 Store。
func (m *Manager) Snapshot() Config {
	if c := m.ptr.Load(); c != nil {
		return cloneConfig(*c)
	}
	return Config{}
}

// Enabled 报告云盘下载总开关当前是否开启。
func (m *Manager) Enabled() bool {
	return m.Snapshot().Enabled
}

// ResolveCloudDestination 按名称解析已启用的目的地（queue 云盘任务在
// Job 运行时取目的地的最小接口；不存在或未启用返回 false）。
// 只做目的地级判定：全局开关在准入层把关，任务已在途时管理员关闭
// 目的地不应静默跳过，而应让本次上传以明确错误收尾。
func (m *Manager) ResolveCloudDestination(name string) (Destination, bool) {
	return m.Snapshot().EnabledDestination(name)
}

// Path 报告底层配置文件路径（供装配层探测与运维排查；不含凭据内容）。
func (m *Manager) Path() string { return m.path }
