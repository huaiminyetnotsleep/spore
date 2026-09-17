package notifycfg

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/notify"
)

type policySettings struct {
	mu     sync.Mutex
	values map[string]string
	writes map[string]int
}

func newPolicySettings() *policySettings {
	return &policySettings{values: map[string]string{}, writes: map[string]int{}}
}

func (s *policySettings) GetSetting(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	return value, ok, nil
}

func (s *policySettings) SetSetting(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	s.writes[key]++
	return nil
}

func TestPolicyDefaultSaveAndCredentialSeparation(t *testing.T) {
	ctx := context.Background()
	store := newPolicySettings()
	manager := NewManager(store, testKey('p'), Options{})

	policy, err := manager.PolicySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Version != PolicyVersion || policy.MinimumSeverity != notify.SeverityWarn || len(policy.Categories) == 0 || policy.Events == nil {
		t.Fatalf("unexpected default policy: %+v", policy)
	}

	if err := manager.SaveBot(ctx, BotInput{Token: "123456:abcdefghijklmnopqrstuv", ChatID: "1"}); err != nil {
		t.Fatal(err)
	}
	channelsBefore := store.values[SettingKey]
	policy.MinimumSeverity = notify.SeverityError
	policy.Events[notify.KeyTempDirUsage] = EventPolicy{
		AdminBadge: OverrideInherit, Bot: OverrideDisabled,
		Webhook: OverrideEnabled, Recovery: OverrideInherit,
	}
	if err := manager.SavePolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if store.values[SettingKey] != channelsBefore {
		t.Fatal("policy save changed notification channel credentials")
	}
	if store.writes[SettingKeyPolicy] != 1 {
		t.Fatalf("policy writes=%d, want 1", store.writes[SettingKeyPolicy])
	}
	got, err := manager.PolicySnapshot(ctx)
	if err != nil || got.MinimumSeverity != notify.SeverityError || got.Events[notify.KeyTempDirUsage].Webhook != OverrideEnabled {
		t.Fatalf("saved policy mismatch: %+v err=%v", got, err)
	}
}

func TestPolicyValidationRejectsUnknownEnums(t *testing.T) {
	manager := NewPolicyManager(newPolicySettings(), time.Now)
	base := DefaultPolicy()
	cases := []Policy{
		func() Policy { p := clonePolicy(base); p.Version = 2; return p }(),
		func() Policy { p := clonePolicy(base); p.MinimumSeverity = "fatal"; return p }(),
		func() Policy { p := clonePolicy(base); p.Categories["unknown"] = CategoryPolicy{}; return p }(),
		func() Policy {
			p := clonePolicy(base)
			p.Events["unknown.event"] = EventPolicy{OverrideInherit, OverrideInherit, OverrideInherit, OverrideInherit}
			return p
		}(),
		func() Policy {
			p := clonePolicy(base)
			p.Events[notify.KeyTempDirUsage] = EventPolicy{"sometimes", OverrideInherit, OverrideInherit, OverrideInherit}
			return p
		}(),
	}
	for i, policy := range cases {
		if err := manager.Save(context.Background(), policy); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestMuteCRUDAndValidation(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	manager := NewPolicyManager(newPolicySettings(), func() time.Time { return now })
	ctx := context.Background()
	created, err := manager.CreateMute(ctx, MuteSchedule{
		Name: "维护窗口", MatchMode: MuteMatchCategory, Category: notify.CategorySystemAlert,
		Channels: []string{ChannelBot, ChannelWebhook}, StartsAt: 0,
		EndsAt: now.Add(time.Hour).UnixMilli(), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("created mute has no ID")
	}
	mutes, err := manager.ListMutes(ctx)
	if err != nil || len(mutes) != 1 || mutes[0].Name != "维护窗口" {
		t.Fatalf("unexpected mutes: %+v err=%v", mutes, err)
	}
	created.Name = "延长维护"
	created.Permanent = true
	created.EndsAt = 0
	updated, err := manager.UpdateMute(ctx, created.ID, created)
	if err != nil || updated.Name != "延长维护" || !updated.Permanent {
		t.Fatalf("unexpected update: %+v err=%v", updated, err)
	}
	if err := manager.DeleteMute(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteMute(ctx, created.ID); !errors.Is(err, ErrMuteNotFound) {
		t.Fatalf("missing delete error=%v", err)
	}

	expired, err := manager.CreateMute(ctx, MuteSchedule{
		Name: "已过期计划", MatchMode: MuteMatchAll, Channels: []string{ChannelBot},
		StartsAt: now.Add(-2 * time.Hour).UnixMilli(), EndsAt: now.Add(-time.Hour).UnixMilli(),
		Enabled: false,
	})
	if err != nil {
		t.Fatalf("disabled expired mute should be retained: %v", err)
	}
	expired.Name = "已停用计划"
	if _, err := manager.UpdateMute(ctx, expired.ID, expired); err != nil {
		t.Fatalf("disabled expired mute should be editable: %v", err)
	}

	invalid := []MuteSchedule{
		{Name: "", MatchMode: MuteMatchAll, Channels: []string{ChannelBot}, Permanent: true},
		{Name: "bad category", MatchMode: MuteMatchCategory, Category: "unknown", Channels: []string{ChannelBot}, Permanent: true},
		{Name: "bad event", MatchMode: MuteMatchEvents, EventTypes: []string{"unknown"}, Channels: []string{ChannelBot}, Permanent: true},
		{Name: "bad channel", MatchMode: MuteMatchAll, Channels: []string{"email"}, Permanent: true},
		{Name: "past", MatchMode: MuteMatchAll, Channels: []string{ChannelBot}, EndsAt: now.UnixMilli(), Enabled: true},
	}
	for _, mute := range invalid {
		if _, err := manager.CreateMute(ctx, mute); err == nil {
			t.Errorf("expected invalid mute rejection: %+v", mute)
		}
	}
}

func TestEvaluatePolicy(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	policy := DefaultPolicy()

	if got := Evaluate(policy, nil, notify.KeyTempDirUsage, ChannelBot, false, true, now); !got.Enabled {
		t.Fatalf("warn bot should be enabled: %+v", got)
	}
	policy.MinimumSeverity = notify.SeverityError
	if got := Evaluate(policy, nil, notify.KeyTempDirUsage, ChannelBot, false, true, now); got.Reason != DecisionBelowSeverity {
		t.Fatalf("warn should be below error threshold: %+v", got)
	}
	policy.Events[notify.KeyTempDirUsage] = EventPolicy{
		AdminBadge: OverrideInherit, Bot: OverrideEnabled,
		Webhook: OverrideInherit, Recovery: OverrideDisabled,
	}
	if got := Evaluate(policy, nil, notify.KeyTempDirUsage, ChannelBot, false, true, now); !got.Enabled {
		t.Fatalf("explicit enabled should override severity: %+v", got)
	}
	if got := Evaluate(policy, nil, notify.KeyTempDirUsage, ChannelBot, true, true, now); got.Reason != DecisionRecoveryDisabled {
		t.Fatalf("recovery override should suppress: %+v", got)
	}
	mutes := []MuteSchedule{{
		ID: "m1", Name: "all", MatchMode: MuteMatchAll, Channels: []string{ChannelBot},
		Permanent: true, Enabled: true,
	}}
	if got := Evaluate(policy, mutes, notify.KeyTempDirUsage, ChannelBot, false, true, now); got.Reason != DecisionMuted || got.MuteID != "m1" {
		t.Fatalf("mute should win: %+v", got)
	}
	if got := Evaluate(policy, nil, notify.KeyTempDirUsage, ChannelBot, false, false, now); got.Reason != DecisionChannelUnavailable {
		t.Fatalf("unavailable channel should be suppressed: %+v", got)
	}
}
