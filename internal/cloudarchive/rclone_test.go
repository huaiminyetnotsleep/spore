package cloudarchive

// rclone 子进程桥的测试：全部经临时目录中的假 rclone shell 脚本驱动，
// 覆盖成功（rcat/copyto）、参数与环境变量正确性、退出码 + stderr 样例的
// 错误分类、取消路径（子进程终止 + 残件清理）与进度解析容错。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// observeDir 建立假 rclone 的观测目录（脚本把 env/args/stdin 记录进去）。
func observeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("OBSERVE_DIR", dir)
	return dir
}

// fakeRclone 生成可执行的假 rclone 脚本并返回路径。
func fakeRclone(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-rclone")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("写入假 rclone 失败: %v", err)
	}
	return path
}

func testDest() Destination {
	return Destination{
		Name: "mega-1", Type: "mega", Enabled: true, PathPrefix: "spore",
		Options: map[string]string{"user": "u@example.com", "pass": "SECRET-OBSCURED-VALUE", "2fa": "123456"},
	}
}

func readObserved(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		t.Fatalf("读取观测文件 %s 失败: %v", name, err)
	}
	return string(data)
}

func TestRemoteEnvMapping(t *testing.T) {
	env := RemoteEnv(testDest())
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"RCLONE_CONFIG_MEGA-1_TYPE=mega",
		"RCLONE_CONFIG_MEGA-1_USER=u@example.com",
		"RCLONE_CONFIG_MEGA-1_PASS=SECRET-OBSCURED-VALUE",
		"RCLONE_CONFIG_MEGA-1_2FA=123456",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("环境变量应包含 %s，得到:\n%s", want, joined)
		}
	}
	if len(env) != 4 {
		t.Errorf("应恰好 4 个环境变量（TYPE + 3 options），得到 %d", len(env))
	}
}

// rcat 成功：参数正确（rcat <name>:<path> --size N + 日志 flags）、
// 环境变量注入、stdin 内容完整送达、stats JSON 进度回调。
func TestRcloneUploadRcatSuccess(t *testing.T) {
	dir := observeDir(t)
	bin := fakeRclone(t, `
env >> "$OBSERVE_DIR/env.txt"
printf '%s\n' "$@" >> "$OBSERVE_DIR/args.txt"
cat > "$OBSERVE_DIR/stdin.txt"
echo '{"level":"info","msg":"stats","stats":{"bytes":5}}' >&2
echo '{"level":"info","msg":"stats","stats":{"bytes":11}}' >&2
exit 0
`)
	sink := &RcloneSink{Bin: bin, Log: testLog()}

	var mu sync.Mutex
	var last int64
	err := sink.Upload(context.Background(), testDest(), UploadSpec{
		RemotePath: "spore/example/2026-09-09/a.bin",
		Size:       11,
		Reader:     strings.NewReader("hello world"),
		OnProgress: func(sent int64) { mu.Lock(); last = sent; mu.Unlock() },
	})
	if err != nil {
		t.Fatalf("上传应成功: %v", err)
	}
	mu.Lock()
	got := last
	mu.Unlock()
	if got != 11 {
		t.Errorf("进度应回调到 11，得到 %d", got)
	}
	if stdin := readObserved(t, dir, "stdin.txt"); stdin != "hello world" {
		t.Errorf("stdin 内容不符: %q", stdin)
	}
	args := readObserved(t, dir, "args.txt")
	for _, want := range []string{"rcat", "mega-1:spore/example/2026-09-09/a.bin",
		"--size", "11", "--use-json-log", "--stats", "1s", "--stats-one-line", "-v"} {
		if !strings.Contains(args, want) {
			t.Errorf("参数应包含 %q，得到: %s", want, args)
		}
	}
	env := readObserved(t, dir, "env.txt")
	if !strings.Contains(env, "RCLONE_CONFIG_MEGA-1_TYPE=mega") ||
		!strings.Contains(env, "RCLONE_CONFIG_MEGA-1_2FA=123456") {
		t.Errorf("子进程环境应注入 rclone 配置变量:\n%s", env)
	}
}

// copyto 成功：临时文件路径直传，不消费 stdin，远端引用正确。
func TestRcloneUploadCopytoSuccess(t *testing.T) {
	dir := observeDir(t)
	bin := fakeRclone(t, `
printf '%s\n' "$*" >> "$OBSERVE_DIR/args.txt"
if [ "$1" = "copyto" ]; then cp "$2" "$OBSERVE_DIR/copied.bin"; fi
exit 0
`)
	sink := &RcloneSink{Bin: bin, Log: testLog()}
	tmp := filepath.Join(t.TempDir(), "big-file.bin")
	if err := os.WriteFile(tmp, []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("写临时文件失败: %v", err)
	}

	err := sink.Upload(context.Background(), testDest(), UploadSpec{
		RemotePath: "spore/example/2026-09-09/big-file.bin",
		Size:       10,
		FilePath:   tmp,
	})
	if err != nil {
		t.Fatalf("上传应成功: %v", err)
	}
	args := readObserved(t, dir, "args.txt")
	if !strings.Contains(args, "copyto "+tmp+" mega-1:spore/example/2026-09-09/big-file.bin") {
		t.Errorf("copyto 参数不符: %s", args)
	}
	if copied := readObserved(t, dir, "copied.bin"); copied != "0123456789" {
		t.Errorf("文件内容应被读取: %q", copied)
	}
}

// 失败分类表：stderr 样例（真机采样后回填，research ⚑5）→ CLOUD_* 码，
// 且每次失败后都发起 deletefile 残件清理。
func TestRcloneUploadClassifiesErrors(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		wantCode apperr.Code
	}{
		{"凭据错误", "Failed to create file system: user or password is incorrect", apperr.CodeCloudAuthFailed},
		{"登录失败", "mega: login failed for user", apperr.CodeCloudAuthFailed},
		// 真机采样：MEGA session 失效/账号被拒时的 critical 日志（2026-09-22 生产）
		{"MEGA 登录被拒", `Failed to create file system for "mega-imnpc:p/2026-09-11/520/": couldn't login: Object (typically, node or user) not found`, apperr.CodeCloudAuthFailed},
		{"配额满", "Storage quota exceeded (over quota)", apperr.CodeCloudQuota},
		{"空间不足", "failed: not enough space", apperr.CodeCloudQuota},
		{"网络超时", "dial tcp 1.2.3.4:443: i/o timeout", apperr.CodeCloudNetwork},
		{"DNS", "no such host", apperr.CodeCloudNetwork},
		{"未知兜底", "some unexpected weird failure", apperr.CodeCloudUploadFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := observeDir(t)
			t.Setenv("FAILMSG", tt.stderr)
			bin := fakeRclone(t, `
if [ "$1" = "deletefile" ]; then printf '%s\n' "$2" >> "$OBSERVE_DIR/delete.txt"; exit 0; fi
echo "$FAILMSG" >&2
exit 7
`)
			sink := &RcloneSink{Bin: bin, Log: testLog()}
			err := sink.Upload(context.Background(), testDest(), UploadSpec{
				RemotePath: "spore/example/a.bin", Size: 4,
				Reader: strings.NewReader("data"),
			})
			ae := apperr.From(err)
			if ae.Code != tt.wantCode {
				t.Fatalf("应归类 %s，得到 %s（%v）", tt.wantCode, ae.Code, err)
			}
			// 错误信息不得携带 options 值（脱敏红线）
			if strings.Contains(err.Error(), "SECRET-OBSCURED-VALUE") {
				t.Fatalf("错误信息泄漏凭据值: %v", err)
			}
			deleted := readObserved(t, dir, "delete.txt")
			if !strings.Contains(deleted, "mega-1:spore/example/a.bin") {
				t.Errorf("失败后应 best-effort 删除残件，观测到: %q", deleted)
			}
		})
	}
}

// 取消：子进程被终止，Upload 返回 ctx.Err()（取消不算上传失败），
// 残件清理仍尽力执行。
func TestRcloneUploadCancel(t *testing.T) {
	dir := observeDir(t)
	bin := fakeRclone(t, `
if [ "$1" = "deletefile" ]; then printf '%s\n' "$2" >> "$OBSERVE_DIR/delete.txt"; exit 0; fi
sleep 30
`)
	sink := &RcloneSink{Bin: bin, Log: testLog()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- sink.Upload(ctx, testDest(), UploadSpec{
			RemotePath: "spore/example/a.bin", Size: 4,
			Reader: strings.NewReader("data"),
		})
	}()
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 Upload 应及时返回")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应返回 ctx.Err()，得到 %v", err)
	}
	if ae, ok := err.(*apperr.AppError); ok {
		_ = ae
		t.Fatal("取消不得包装为云盘上传错误")
	}
	for i := 0; i < 100; i++ {
		if strings.Contains(readObserved(t, dir, "delete.txt"), "mega-1:spore/example/a.bin") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("取消后应尽力清理远端残件")
}

// stats 解析失败时 rcat 路径退化为 stdin 写入计数。
func TestRcloneUploadFallbackProgress(t *testing.T) {
	dir := observeDir(t)
	bin := fakeRclone(t, `
cat > "$OBSERVE_DIR/stdin.txt"
echo "plain text stderr line" >&2
exit 0
`)
	sink := &RcloneSink{Bin: bin, Log: testLog()}
	var last int64
	err := sink.Upload(context.Background(), testDest(), UploadSpec{
		RemotePath: "spore/example/a.bin", Size: 11,
		Reader:     strings.NewReader("hello world"),
		OnProgress: func(sent int64) { last = sent },
	})
	if err != nil {
		t.Fatalf("上传应成功: %v", err)
	}
	if last != 11 {
		t.Errorf("stdin 兜底计数应推进到 11，得到 %d", last)
	}
	if stdin := readObserved(t, dir, "stdin.txt"); stdin != "hello world" {
		t.Errorf("stdin 内容不符: %q", stdin)
	}
}

// stats JSON 字段容错：stats.bytes / stats.bytes_transferred / 顶层 bytes。
func TestParseStatsBytes(t *testing.T) {
	tests := []struct {
		line string
		want int64
		ok   bool
	}{
		{`{"level":"info","msg":"stats","stats":{"bytes":1024}}`, 1024, true},
		{`{"stats":{"bytes_transferred":2048}}`, 2048, true},
		{`{"msg":"stats","bytes":4096}`, 4096, true},
		{`{"msg":"other","bytes":4096}`, 0, false}, // 顶层 bytes 仅 stats 行生效
		{`{"level":"error","msg":"failed to..."}`, 0, false},
		{`plain text line`, 0, false},
	}
	for _, tt := range tests {
		got, ok := parseStatsBytes(tt.line)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("parseStatsBytes(%s) = (%d,%v), want (%d,%v)", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}

// stderr 管道分片到达（一行 JSON 被拆到多次 Write）不得拆散 JSON 解析，
// 进度只前进不回退（单调门）。
func TestStatsCollectorSplitWritesAndMonotonic(t *testing.T) {
	var mu sync.Mutex
	var got []int64
	c := newStatsCollector(func(sent int64) { mu.Lock(); got = append(got, sent); mu.Unlock() })

	line := `{"level":"info","msg":"stats","stats":{"bytes":777}}`
	for i := 0; i < len(line); i++ { // 逐字节分片写入
		if _, err := c.Write([]byte{line[i]}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	if _, err := c.Write([]byte("\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 回退值（另一来源交错上报更低绝对值）不得回调
	if _, err := c.Write([]byte(`{"stats":{"bytes":100}}` + "\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != 777 {
		t.Fatalf("应恰好回调一次 777（分片不拆散、回退不触发）: %v", got)
	}
	if tail := c.tailText(); !strings.Contains(tail, `"bytes":777`) {
		t.Errorf("尾部留存应包含完整行: %q", tail)
	}
}

func TestRclonePing(t *testing.T) {
	dir := observeDir(t)
	bin := fakeRclone(t, `
printf '%s\n' "$*" >> "$OBSERVE_DIR/args.txt"
exit 0
`)
	sink := &RcloneSink{Bin: bin, Log: testLog()}
	if err := sink.Ping(context.Background(), testDest()); err != nil {
		t.Fatalf("Ping 应成功: %v", err)
	}
	if args := readObserved(t, dir, "args.txt"); !strings.Contains(args, "lsd mega-1: --max-depth 1") {
		t.Errorf("Ping 参数不符: %s", args)
	}

	t.Setenv("FAILMSG", "user or password is incorrect")
	failBin := fakeRclone(t, `echo "$FAILMSG" >&2; exit 5`)
	sink2 := &RcloneSink{Bin: failBin, Log: testLog()}
	err := sink2.Ping(context.Background(), testDest())
	if ae := apperr.From(err); ae.Code != apperr.CodeCloudAuthFailed {
		t.Fatalf("Ping 凭据错误应归类 CLOUD_AUTH_FAILED，得到 %v", err)
	}

	// 探测超时：归 CLOUD_NETWORK（日志 code 与用户文案一致），且保留
	// cause 链（cloudTestFailureText 靠 errors.Is 判超时给网络文案）。
	hangBin := fakeRclone(t, `sleep 5`)
	sink3 := &RcloneSink{Bin: hangBin, Log: testLog()}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = sink3.Ping(ctx, testDest())
	if ae := apperr.From(err); ae.Code != apperr.CodeCloudNetwork {
		t.Fatalf("Ping 超时应归类 CLOUD_NETWORK，得到 %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Ping 超时应保留 DeadlineExceeded cause 链: %v", err)
	}
}

// Obscure 从 stdin 读取明文，并将 rclone 的混淆输出返回给调用方。
func TestObscureUsesStdinAndReturnsOutput(t *testing.T) {
	dir := observeDir(t)
	bin := fakeRclone(t, `
if [ "$1" != "obscure" ] || [ "$2" != "-" ]; then exit 9; fi
cat > "$OBSERVE_DIR/obscure-input.txt"
printf 'obscured-value\n'
`)
	t.Setenv("RCLONE_BIN", bin)
	got, err := Obscure(context.Background(), `p@ss&word$'quoted'`)
	if err != nil {
		t.Fatalf("Obscure 应成功: %v", err)
	}
	if got != "obscured-value" {
		t.Fatalf("Obscure 输出不符: %q", got)
	}
	if input := readObserved(t, dir, "obscure-input.txt"); input != `p@ss&word$'quoted'` {
		t.Fatalf("明文应通过 stdin 传入，得到 %q", input)
	}
}

func TestObscureFailureDoesNotExposePlaintext(t *testing.T) {
	secret := `p@ss&word$'quoted'`
	bin := fakeRclone(t, `cat >/dev/null; exit 7`)
	t.Setenv("RCLONE_BIN", bin)
	_, err := Obscure(context.Background(), secret)
	if err == nil {
		t.Fatal("Obscure 失败时应返回错误")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("错误不得泄漏明文: %v", err)
	}
}

func TestBinPathOverride(t *testing.T) {
	t.Setenv("RCLONE_BIN", "/no/such/rclone")
	if _, err := BinPath(); err == nil {
		t.Fatal("RCLONE_BIN 指向不存在路径应报错")
	}
	sink := &RcloneSink{Log: testLog()} // 走 BinPath 定位
	err := sink.Upload(context.Background(), testDest(), UploadSpec{
		RemotePath: "x", Reader: strings.NewReader("d"),
	})
	if ae := apperr.From(err); ae.Code != apperr.CodeCloudUploadFailed {
		t.Fatalf("二进制不可用应归类 CLOUD_UPLOAD_FAILED，得到 %v", err)
	}

	bin := fakeRclone(t, "exit 0")
	t.Setenv("RCLONE_BIN", bin)
	got, err := BinPath()
	if err != nil || got != bin {
		t.Fatalf("RCLONE_BIN 指向可用脚本应返回该路径: %s %v", got, err)
	}
}

// 数据源缺失的防御：Reader 与 FilePath 均空 → INTERNAL_ERROR。
func TestUploadMissingSource(t *testing.T) {
	sink := &RcloneSink{Bin: fakeRclone(t, "exit 0"), Log: testLog()}
	err := sink.Upload(context.Background(), testDest(), UploadSpec{RemotePath: "x"})
	if ae := apperr.From(err); ae.Code != apperr.CodeInternal {
		t.Fatalf("缺数据源应归类 INTERNAL_ERROR，得到 %v", err)
	}
}

// ---- 已上传核验（VerifyUploaded）----

func TestGroupRemotePaths(t *testing.T) {
	groups := groupRemotePaths([]string{
		"spore/example/2026-09-09/7/01_a.bin",
		"spore/example/2026-09-09/7/caption.txt",
		"spore/example/2026-09-09/b.bin",
		"root.bin",
		"spore/example/2026-09-09/7/", // 尾斜杠：文件名为空，忽略
	})
	if len(groups) != 3 {
		t.Fatalf("应按三个父目录分组，得到 %d: %v", len(groups), groups)
	}
	if got := groups["spore/example/2026-09-09/7"]; len(got) != 2 ||
		got[0] != "01_a.bin" || got[1] != "caption.txt" {
		t.Errorf("相册目录成员不符: %v", got)
	}
	if got := groups["spore/example/2026-09-09"]; len(got) != 1 || got[0] != "b.bin" {
		t.Errorf("平铺目录成员不符: %v", got)
	}
	if got := groups[""]; len(got) != 1 || got[0] != "root.bin" {
		t.Errorf("根目录成员不符: %v", got)
	}
}

// 核验成功：按目录分组、每目录一次 lsjson --files-only、成员齐全 → (true, nil)。
func TestRcloneVerifyUploadedSuccess(t *testing.T) {
	dir := observeDir(t)
	bin := fakeRclone(t, `
printf '%s\n' "$*" >> "$OBSERVE_DIR/args.txt"
if [ "$2" = "mega-1:spore/example/2026-09-09/7" ]; then
  printf '[{"Path":"01_a.bin","Name":"01_a.bin","Size":10},{"Path":"caption.txt","Name":"caption.txt","Size":3}]'
  exit 0
fi
printf '[]'
exit 0
`)
	sink := &RcloneSink{Bin: bin, Log: testLog()}
	ok, err := sink.VerifyUploaded(context.Background(), testDest(), []string{
		"spore/example/2026-09-09/7/01_a.bin",
		"spore/example/2026-09-09/7/caption.txt",
	})
	if err != nil || !ok {
		t.Fatalf("全部存在应返回 (true, nil)，得到 (%v, %v)", ok, err)
	}
	args := readObserved(t, dir, "args.txt")
	if !strings.Contains(args, "lsjson mega-1:spore/example/2026-09-09/7 --files-only") {
		t.Errorf("lsjson 参数不符: %s", args)
	}
	if lines := strings.Count(strings.TrimSpace(args), "\n") + 1; lines != 1 {
		t.Errorf("同目录成员应只列举一次，观测到 %d 次调用: %s", lines, args)
	}
}

// 成员缺失与目录不存在均属"确定不存在"：(false, nil)，不算核验失败。
func TestRcloneVerifyUploadedMissing(t *testing.T) {
	// 目录存在但缺少 caption.txt
	bin := fakeRclone(t, `printf '[{"Name":"01_a.bin"}]'`)
	sink := &RcloneSink{Bin: bin, Log: testLog()}
	ok, err := sink.VerifyUploaded(context.Background(), testDest(), []string{
		"spore/example/2026-09-09/7/01_a.bin",
		"spore/example/2026-09-09/7/caption.txt",
	})
	if err != nil || ok {
		t.Fatalf("成员缺失应返回 (false, nil)，得到 (%v, %v)", ok, err)
	}

	// 目录不存在（stderr 命中 not found 模式）
	t.Setenv("FAILMSG", "Failed to lsjson: directory not found")
	bin2 := fakeRclone(t, `echo "$FAILMSG" >&2; exit 3`)
	sink2 := &RcloneSink{Bin: bin2, Log: testLog()}
	ok, err = sink2.VerifyUploaded(context.Background(), testDest(), []string{
		"spore/example/2026-09-09/gone.bin",
	})
	if err != nil || ok {
		t.Fatalf("目录不存在应返回 (false, nil)，得到 (%v, %v)", ok, err)
	}
}

// 核验本身失败（网络/凭据/输出非法）：返回错误，存在性未知。
func TestRcloneVerifyUploadedCheckFailed(t *testing.T) {
	t.Setenv("FAILMSG", "dial tcp 1.2.3.4:443: i/o timeout")
	bin := fakeRclone(t, `echo "$FAILMSG" >&2; exit 7`)
	sink := &RcloneSink{Bin: bin, Log: testLog()}
	ok, err := sink.VerifyUploaded(context.Background(), testDest(), []string{
		"spore/example/2026-09-09/a.bin",
	})
	if ok {
		t.Fatal("核验失败时不得报告存在")
	}
	if ae := apperr.From(err); ae.Code != apperr.CodeCloudNetwork {
		t.Fatalf("网络失败应归类 CLOUD_NETWORK，得到 %v", err)
	}

	badJSON := fakeRclone(t, `printf 'not-json'`)
	sink2 := &RcloneSink{Bin: badJSON, Log: testLog()}
	if _, err := sink2.VerifyUploaded(context.Background(), testDest(), []string{
		"spore/example/2026-09-09/a.bin",
	}); apperr.From(err).Code == apperr.CodeInternal {
		t.Fatalf("非法输出应返回云盘错误而非 INTERNAL: %v", err)
	}

	empty := &RcloneSink{Bin: fakeRclone(t, "exit 0"), Log: testLog()}
	if _, err := empty.VerifyUploaded(context.Background(), testDest(), nil); apperr.From(err).Code != apperr.CodeInternal {
		t.Fatalf("空路径列表应归类 INTERNAL_ERROR，得到 %v", err)
	}
}

// 取消：核验返回 ctx.Err()，不算云盘错误。
func TestRcloneVerifyUploadedCancel(t *testing.T) {
	bin := fakeRclone(t, `sleep 30`)
	sink := &RcloneSink{Bin: bin, Log: testLog()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := sink.VerifyUploaded(ctx, testDest(), []string{"spore/example/a.bin"})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消应返回 ctx.Err()，得到 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后核验应及时返回")
	}
}

// ProviderHomeSite：已知后端返回官网，未知类型返回空串（文案省略官网行）。
func TestProviderHomeSite(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"mega", "https://mega.nz/"},
		{"MEGA", "https://mega.nz/"},
		{" Mega ", "https://mega.nz/"},
		{"s3", ""},
		{"webdav", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := ProviderHomeSite(tc.in); got != tc.want {
			t.Errorf("ProviderHomeSite(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
