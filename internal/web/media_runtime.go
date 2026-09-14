package web

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// ApplyRuntimeMediaConfig 在数据库打开后、媒体依赖组装前读取 Web 设置覆盖值。
// 没有覆盖时保留环境配置；覆盖值不兼容部署能力时返回错误并阻止静默启动。
func ApplyRuntimeMediaConfig(ctx context.Context, st *store.Store, cfg config.Config) (config.Config, error) {
	maxSize, stream, tempDirMax := cfg.MaxFileSize, cfg.StreamLimit, cfg.TempDirMaxSize
	if raw, ok, err := st.GetSetting(ctx, settingKeyMaxFileSize); err != nil {
		return cfg, err
	} else if ok {
		if err := json.Unmarshal([]byte(raw), &maxSize); err != nil {
			return cfg, fmt.Errorf("媒体文件大小上限设置无效")
		}
	}
	if raw, ok, err := st.GetSetting(ctx, settingKeyStreamLimit); err != nil {
		return cfg, err
	} else if ok {
		if err := json.Unmarshal([]byte(raw), &stream); err != nil {
			return cfg, fmt.Errorf("媒体流式阈值设置无效")
		}
	}
	if raw, ok, err := st.GetSetting(ctx, settingKeyTempDirMaxSize); err != nil {
		return cfg, err
	} else if ok {
		if err := json.Unmarshal([]byte(raw), &tempDirMax); err != nil {
			return cfg, fmt.Errorf("临时目录最大大小设置无效")
		}
	}
	if maxSize == 0 || stream == 0 {
		return cfg, fmt.Errorf("媒体运行配置缺少文件大小上限或流式阈值")
	}
	if tempDirMax == 0 {
		tempDirMax = int64(5) << 30
	}
	if err := config.ValidateMediaLimits(maxSize, stream, tempDirMax); err != nil {
		return cfg, err
	}
	cfg.MaxFileSize, cfg.StreamLimit, cfg.TempDirMaxSize = maxSize, stream, tempDirMax
	if cfg.BotAPIURL != "" {
		cfg.PhotoLimit = maxSize
	}
	return cfg, nil
}
