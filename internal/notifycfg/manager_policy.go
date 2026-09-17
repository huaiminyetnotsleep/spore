package notifycfg

import "context"

// PolicySnapshot returns the credential-free notification policy.
func (m *Manager) PolicySnapshot(ctx context.Context) (Policy, error) {
	return m.policy.Snapshot(ctx)
}

// SavePolicy validates and atomically replaces only notification_policy. It
// never reads or writes notification_channels credentials.
func (m *Manager) SavePolicy(ctx context.Context, policy Policy) error {
	return m.policy.Save(ctx, policy)
}

func (m *Manager) ListMutes(ctx context.Context) ([]MuteSchedule, error) {
	return m.policy.ListMutes(ctx)
}

func (m *Manager) CreateMute(ctx context.Context, mute MuteSchedule) (MuteSchedule, error) {
	return m.policy.CreateMute(ctx, mute)
}

func (m *Manager) UpdateMute(ctx context.Context, id string, mute MuteSchedule) (MuteSchedule, error) {
	return m.policy.UpdateMute(ctx, id, mute)
}

func (m *Manager) DeleteMute(ctx context.Context, id string) error {
	return m.policy.DeleteMute(ctx, id)
}
