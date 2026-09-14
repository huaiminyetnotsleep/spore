package cloudarchive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func validConfig() Config {
	return Config{
		Enabled:            true,
		DefaultDestination: "mega-1",
		Destinations: []Destination{{
			Name: "mega-1", Type: "mega", Enabled: true, PathPrefix: "spore",
			Options: map[string]string{"user": "u@example.com", "pass": "obscured-value"},
		}},
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // 空串表示应通过
	}{
		{"完整配置通过", func(*Config) {}, ""},
		{"名称非法-大写", func(c *Config) { c.Destinations[0].Name = "Mega" }, "目的地名称不合法"},
		{"名称非法-下划线", func(c *Config) { c.Destinations[0].Name = "mega_1" }, "目的地名称不合法"},
		{"名称非法-数字开头", func(c *Config) { c.Destinations[0].Name = "1mega" }, "目的地名称不合法"},
		{"名称超长", func(c *Config) { c.Destinations[0].Name = "m" + strings.Repeat("a", 32) }, "目的地名称不合法"},
		{"名称重复", func(c *Config) {
			c.Destinations = append(c.Destinations, Destination{Name: "mega-1", Type: "mega"})
		}, "目的地名称重复"},
		{"类型为空", func(c *Config) { c.Destinations[0].Type = " " }, "缺少类型"},
		{"路径前缀绝对路径", func(c *Config) { c.Destinations[0].PathPrefix = "/outside" }, "不能是绝对路径"},
		{"路径前缀路径穿越", func(c *Config) { c.Destinations[0].PathPrefix = "spore/../outside" }, "不能包含 .. 路径段"},
		{"options 空值", func(c *Config) { c.Destinations[0].Options["pass"] = "" }, "值为空"},
		{"默认目的地不存在", func(c *Config) { c.DefaultDestination = "mega-2" }, "不存在或未启用"},
		{"默认目的地未启用", func(c *Config) { c.Destinations[0].Enabled = false }, "不存在或未启用"},
		{"开启但无目的地", func(c *Config) { c.Destinations = nil }, "至少需要配置一个目的地"},
		{"开启但无默认", func(c *Config) { c.DefaultDestination = "" }, "需要指定默认目的地"},
		// 草稿态（enabled=false）：门禁类问题放行
		{"草稿允许默认悬空", func(c *Config) {
			c.Enabled = false
			c.DefaultDestination = "gone"
		}, ""},
		{"草稿允许无目的地", func(c *Config) {
			c.Enabled = false
			c.Destinations = nil
			c.DefaultDestination = ""
		}, ""},
		// 结构性问题在草稿态仍报错（管理台保存时即时反馈）
		{"草稿仍拒绝非法名称", func(c *Config) {
			c.Enabled = false
			c.Destinations[0].Name = "BAD"
		}, "目的地名称不合法"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(&c)
			err := c.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("应校验通过，得到: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("应报含 %q 的错误，得到: %v", tt.wantErr, err)
			}
		})
	}
}

func TestConfigFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	cfg := validConfig()

	if err := SaveFile(path, cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	// 权限必须是 0600（含网盘凭据）
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取文件信息失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("配置文件权限应为 0600，得到 %o", perm)
	}
	// 目录内不残留临时文件
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("原子写不应残留临时文件: %v", entries)
	}

	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if !got.Enabled || got.DefaultDestination != "mega-1" || len(got.Destinations) != 1 {
		t.Fatalf("配置往返不一致: %+v", got)
	}
	d := got.Destinations[0]
	if d.Type != "mega" || d.Options["user"] != "u@example.com" {
		t.Fatalf("目的地字段往返不一致: %+v", d)
	}
}

// 保存失败时原文件保持不变（原子写：临时文件 + rename）。
func TestConfigSaveFailureKeepsOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	old := validConfig()
	if err := SaveFile(path, old); err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}
	broken := Config{Enabled: true, DefaultDestination: "none"} // 校验失败
	if err := SaveFile(path, broken); err == nil {
		t.Fatal("非法配置应保存失败")
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if got.DefaultDestination != old.DefaultDestination {
		t.Fatalf("保存失败不得改动原文件: %+v", got)
	}
}

func TestLoadFileMissingIsZeroConfig(t *testing.T) {
	cfg, err := LoadFile(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatalf("文件不存在应返回零值配置且无错误: %v", err)
	}
	if cfg.Enabled || len(cfg.Destinations) != 0 {
		t.Fatalf("零值配置不对: %+v", cfg)
	}
}

// 损坏 JSON：LoadFile 报错；Manager.Load 保持旧快照不变。
func TestLoadFileCorruptedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := SaveFile(path, validConfig()); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("写入损坏内容失败: %v", err)
	}

	if _, err := LoadFile(path); err == nil {
		t.Fatal("损坏 JSON 应报错")
	}
	m := NewManager(path, nil)
	if m.Snapshot().Enabled {
		t.Fatal("加载失败应保持零值快照（功能关闭）")
	}
	if m.Enabled() {
		t.Error("Enabled 应为 false")
	}
}

func TestManagerSaveAndResolve(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	m := NewManager(path, nil)

	if m.Snapshot().Enabled {
		t.Fatal("未配置时快照应为零值")
	}
	if err := m.Save(validConfig()); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if !m.Enabled() {
		t.Fatal("保存后快照应已替换")
	}
	d, ok := m.ResolveCloudDestination("mega-1")
	if !ok || d.Type != "mega" || d.PathPrefix != "spore" {
		t.Fatalf("应解析到已启用目的地: %+v ok=%v", d, ok)
	}
	if _, ok := m.ResolveCloudDestination("mega-2"); ok {
		t.Fatal("不存在的目的地不应解析成功")
	}

	// 非法配置拒绝保存且快照不变
	bad := validConfig()
	bad.Destinations[0].Name = "UPPER"
	if err := m.Save(bad); err == nil {
		t.Fatal("非法名称应保存失败")
	}
	if m.Snapshot().Destinations[0].Name != "mega-1" {
		t.Fatal("保存失败不得替换快照")
	}

	// Load 从磁盘恢复（模拟外部编辑后重读）
	m2 := NewManager(path, nil)
	if !m2.Enabled() {
		t.Fatal("从磁盘加载应得到已启用配置")
	}
}

// Manager 快照读取无锁：并发读 + 保存替换在 -race 下安全。
func TestManagerConcurrentSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	m := NewManager(path, nil)
	if err := m.Save(validConfig()); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = m.Snapshot().Enabled
				_, _ = m.ResolveCloudDestination("mega-1")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 20; j++ {
			_ = m.Load()
		}
	}()
	wg.Wait()
}

// JSON 字段名与 web 端和管理台共用的文件契约一致。
func TestConfigJSONShape(t *testing.T) {
	data, err := json.Marshal(validConfig())
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	for _, key := range []string{`"enabled"`, `"default_destination"`, `"destinations"`,
		`"name"`, `"type"`, `"path_prefix"`, `"options"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("JSON 应包含字段 %s: %s", key, data)
		}
	}
}
