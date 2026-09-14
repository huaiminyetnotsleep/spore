package tmeurl

import "testing"

func assertHash(t *testing.T, got string, ok bool, want string) {
	t.Helper()
	if !ok {
		t.Fatalf("期望解析成功，实际失败")
	}
	if got != want {
		t.Fatalf("解析结果不符\nwant: %s\ngot:  %s", want, got)
	}
}

func TestParseInvitePlusForm(t *testing.T) {
	for _, input := range []string{
		"https://t.me/+AbCdEfGh12345678",
		"t.me/+AbCdEfGh12345678",
		"https://www.t.me/+AbCdEfGh12345678",
		"https://telegram.me/+AbCdEfGh12345678",
		"HTTPS://T.ME/+AbCdEfGh12345678",
		"请加入 https://t.me/+AbCdEfGh12345678 谢谢",
		"https://t.me/+AbCdEfGh12345678。",
	} {
		got, ok := ParseInvite(input)
		assertHash(t, got, ok, "AbCdEfGh12345678")
	}
}

func TestParseInviteAllowsServerValidatedShortHash(t *testing.T) {
	got, ok := ParseInvite("https://t.me/+short")
	assertHash(t, got, ok, "short")
}

func TestParseInvitePreservesTrailingHashCharacters(t *testing.T) {
	for _, hash := range []string{"AbCdEfGh1234567-", "AbCdEfGh1234567_"} {
		got, ok := ParseInvite("https://t.me/+" + hash)
		assertHash(t, got, ok, hash)
	}
}

func TestParseInviteJoinchatForm(t *testing.T) {
	for _, input := range []string{
		"https://t.me/joinchat/AbCdEfGh12345678",
		"t.me/joinchat/AbCdEfGh12345678",
		"https://telegram.me/joinchat/AbCdEfGh12345678",
	} {
		got, ok := ParseInvite(input)
		assertHash(t, got, ok, "AbCdEfGh12345678")
	}
}

func TestParseInviteBareHash(t *testing.T) {
	got, ok := ParseInvite("AbCdEfGh12345678")
	assertHash(t, got, ok, "AbCdEfGh12345678")

	// 带连字符/下划线的 hash 形态
	got, ok = ParseInvite("AbCdEf_gh-12345678")
	assertHash(t, got, ok, "AbCdEf_gh-12345678")

	// 首尾空白被容忍
	got, ok = ParseInvite("  AbCdEfGh12345678 \n")
	assertHash(t, got, ok, "AbCdEfGh12345678")
}

func TestParseInviteRejectsInvalid(t *testing.T) {
	for _, input := range []string{
		"",                                     // 空
		"   ",                                  // 空白
		"https://t.me/+bad.hash",               // hash 含非法字符
		"https://t.me/+AbCdEfGh12345678/extra", // 多余路径段
		"https://evil.com/+AbCdEfGh12345678",   // 非 t.me 域
		"https://t.me/joinchat",                // joinchat 无 hash
		"https://t.me/joinchat/bad.hash",       // joinchat hash 含非法字符
		"https://t.me/username",                // 普通用户名链接（非邀请）
		"https://t.me/c/1234567890",            // 私有频道 ID 链接（非邀请）
		"notevil.t.me/+AbCdEfGh12345678",       // 边界字符前缀（跳过候选）
		"https://t.me/+AbCdEfGh=12345678",      // hash 含非法字符
	} {
		if _, ok := ParseInvite(input); ok {
			t.Fatalf("期望拒绝 %q，实际解析成功", input)
		}
	}
}

func TestMaskInviteHash(t *testing.T) {
	if got := MaskInviteHash("AbCdEfGh12345678"); got != "AbCd…5678" {
		t.Fatalf("脱敏结果不符: %s", got)
	}
	if got := MaskInviteHash("short"); got != "*****" {
		t.Fatalf("短 hash 应全遮蔽: %s", got)
	}
}
