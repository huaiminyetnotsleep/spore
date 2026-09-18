package notifycfg

// 活动通知的策略兼容测试：存量策略文档（仅两个类别）加载后自动补齐
// activity 类别；活动通知不受最低严重级别门槛约束（info 级不被默认 warn 吞掉）；
// 逐事件覆盖与静音照常生效。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/notify"
)

// legacyPolicyDoc 模拟 activity 类别引入前写入 settings 的策略文档。
func legacyPolicyDoc(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"version":          PolicyVersion,
		"minimum_severity": notify.SeverityWarn,
		"categories": map[string]any{
			"system_alert":    map[string]any{"admin_badge": true, "bot": true, "webhook": false},
			"system_recovery": map[string]any{"admin_badge": true, "bot": true, "webhook": true},
		},
		"events": map[string]any{},
		"mutes":  []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestLoadBackfillsActivityCategory(t *testing.T) {
	st := newPolicySettings()
	st.values[SettingKeyPolicy] = legacyPolicyDoc(t)
	manager := NewPolicyManager(st, time.Now)

	policy, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("存量策略文档应可正常加载，得到 %v", err)
	}
	activity, ok := policy.Categories[notify.CategoryActivity]
	if !ok {
		t.Fatalf("加载后应补齐 activity 类别，得到 %+v", policy.Categories)
	}
	if !activity.AdminBadge || !activity.Bot || !activity.Webhook {
		t.Errorf("补齐类别应默认全渠道开启，得到 %+v", activity)
	}
	// 原有类别配置保持不变（webhook=false 不被覆盖）。
	alert := policy.Categories[notify.CategorySystemAlert]
	if !alert.Bot || alert.Webhook {
		t.Errorf("存量类别配置应保持原值，得到 %+v", alert)
	}
}

func TestEvaluateActivityIgnoresMinimumSeverity(t *testing.T) {
	policy := DefaultPolicy() // MinimumSeverity=warn，类别全开
	now := time.Now()

	got := Evaluate(policy, nil, notify.KeyWebAdminLogin, ChannelBot, false, true, now)
	if !got.Enabled {
		t.Fatalf("默认策略下活动通知应放行（不受 warn 门槛约束），得到 %+v", got)
	}

	// 门槛调到 error：告警事件（warn）被抑制，活动通知不受影响。
	policy.MinimumSeverity = notify.SeverityError
	if got := Evaluate(policy, nil, notify.KeyTempDirUsage, ChannelBot, false, true, now); got.Enabled {
		t.Errorf("warn 级告警在 error 门槛下应被抑制，得到 %+v", got)
	}
	if got := Evaluate(policy, nil, notify.KeyUserApplication, ChannelBot, false, true, now); !got.Enabled {
		t.Errorf("活动通知不受最低严重级别约束，得到 %+v", got)
	}

	// 逐事件覆盖照常生效：显式关闭 bot 渠道后抑制。
	policy.MinimumSeverity = notify.SeverityWarn
	policy.Events[notify.KeyWebAdminLogin] = EventPolicy{
		AdminBadge: OverrideInherit, Bot: OverrideDisabled, Webhook: OverrideInherit, Recovery: OverrideInherit,
	}
	if got := Evaluate(policy, nil, notify.KeyWebAdminLogin, ChannelBot, false, true, now); got.Enabled {
		t.Errorf("事件覆盖 disabled 应抑制活动通知，得到 %+v", got)
	}
}
