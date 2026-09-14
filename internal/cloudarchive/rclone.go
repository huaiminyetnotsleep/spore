package cloudarchive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// UploadSpec 描述一次网盘上传：远端路径（不含 remote 前缀）与数据源。
// Reader（内存/管道句柄）与 FilePath（临时文件句柄）二选一：
// 前者经 `rclone rcat --size N` 走 stdin 流式；后者经 `rclone copyto`
// 直读文件绕开任何缓冲（与 media.Open 三分支对齐）。
type UploadSpec struct {
	RemotePath string           // 完整远端路径（不含 "<name>:" 前缀与 path_prefix 之外的任何改写）
	Size       int64            // 精确字节数（Telegram 报告值；rcat --size 必须与流完全一致）
	Reader     io.Reader        // 内存/管道句柄（与 FilePath 二选一）
	FilePath   string           // 临时文件路径（media 临时文件命名契约）
	OnProgress func(sent int64) // 当前文件已上传字节（绝对值，单调不减）
}

// Sink 是网盘上传通道的抽象（rclone 子进程实现见 RcloneSink；
// 未来本地下载可新增 LocalSink 实现，命令/准入/队列全复用）。
type Sink interface {
	// Upload 把 spec 描述的数据上传到 dest。ctx 取消时终止子进程并
	// best-effort 清理远端残件，返回 ctx.Err()（取消不算上传失败）。
	Upload(ctx context.Context, dest Destination, spec UploadSpec) error
	// Ping 轻量连通性测试（只读），仅在管理台保存/手动测试时调用
	//（MEGA 管理类调用高频会触发限流封禁）。
	Ping(ctx context.Context, dest Destination) error
	// VerifyUploaded 核验 paths（完整远端路径，不含 "<name>:" 前缀）是否
	// 全部仍存在于 dest：按父目录分组，每个目录一次只读 lsjson。返回
	// (true, nil)=全部存在，(false, nil)=有路径确定不存在（远端已删除）；
	// err 非 nil 表示核验本身失败（网络/限流等），存在性未知，调用方应提示
	// 稍后重试，不得据此重传或误报成功。
	VerifyUploaded(ctx context.Context, dest Destination, paths []string) (bool, error)
}

// RcloneSink 经 rclone 子进程上传。Bin 指定二进制路径，空串按
// RCLONE_BIN 环境变量 → PATH 中的 rclone 定位（每次调用前探测存在性）。
type RcloneSink struct {
	Bin string
	Log *slog.Logger
}

// NewRcloneSink 创建 rclone 上传通道；log 为 nil 时用 slog.Default()。
func NewRcloneSink(log *slog.Logger) *RcloneSink {
	if log == nil {
		log = slog.Default()
	}
	return &RcloneSink{Log: log}
}

// envRemoteToken 把 rclone remote 名称转为环境变量 token：字母转大写，
// 连字符保持不变。rclone 的环境变量解析要求 remote 名称与引用名一致，
// 因此 mega-1 必须映射为 MEGA-1，而不是 MEGA_1。
func envRemoteToken(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// envOptionToken 把 rclone 配置键转为环境变量 token：大写，连字符转 `_`。
func envOptionToken(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// RemoteEnv 生成 dest 的 rclone 环境变量（RCLONE_CONFIG_<NAME>_<OPT>，
// 不落 rclone.conf）。返回顺序确定（键排序），便于测试断言。
// 值可能含凭据：仅传给子进程环境，绝不进日志/错误信息。
func RemoteEnv(dest Destination) []string {
	prefix := "RCLONE_CONFIG_" + envRemoteToken(dest.Name) + "_"
	env := []string{prefix + "TYPE=" + dest.Type}
	keys := make([]string, 0, len(dest.Options))
	for k := range dest.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, prefix+envOptionToken(k)+"="+dest.Options[k])
	}
	return env
}

// BinPath 定位 rclone 二进制：RCLONE_BIN 环境变量（须存在且可执行）→
// PATH 中的 rclone。找不到返回错误（调用方据此禁用功能并告警）。
func validateExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("路径是目录")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return errors.New("路径不可执行")
	}
	return nil
}

func BinPath() (string, error) {
	if p := os.Getenv("RCLONE_BIN"); p != "" {
		if err := validateExecutable(p); err != nil {
			return "", fmt.Errorf("RCLONE_BIN 指向的 rclone 不可用: %s", p)
		}
		return p, nil
	}
	p, err := exec.LookPath("rclone")
	if err != nil {
		return "", errors.New("PATH 中未找到 rclone 二进制")
	}
	return p, nil
}

// Obscure 将明文凭据交给 rclone 转换为其配置格式。明文只通过 stdin
// 传递，不进入命令参数、日志或返回错误；调用方负责决定哪些 option 需要转换。
func Obscure(ctx context.Context, plaintext string) (string, error) {
	bin, err := BinPath()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, "obscure", "-")
	cmd.Stdin = strings.NewReader(plaintext)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", errors.New("rclone obscure 执行失败")
	}
	value := strings.TrimSpace(stdout.String())
	if value == "" {
		return "", errors.New("rclone obscure 未返回有效结果")
	}
	return value, nil
}

func (s *RcloneSink) bin() (string, error) {
	if s.Bin != "" {
		if err := validateExecutable(s.Bin); err != nil {
			return "", fmt.Errorf("RcloneSink 指定的 rclone 不可用: %w", err)
		}
		return s.Bin, nil
	}
	return BinPath()
}

// cleanupTimeout 是失败后远端残件清理（deletefile）的尽力而为时间窗：
// 使用剥离取消信号的 ctx，容忍失败不阻塞任务收尾。
const cleanupTimeout = 30 * time.Second

// statsTailLines 是错误分类保留的 stderr 尾部行数（管理端排障用，
// 经 redact 处理后进入 AppError.Message）。
const statsTailLines = 20

// Upload 执行一次上传：
//   - FilePath 非空 → `rclone copyto <file> <name>:<path>`；
//   - 否则 → `rclone rcat <name>:<path> --size N`（stdin 接 Reader）；
//   - 两者统一加 `--use-json-log --stats 1s --stats-one-line -v`，
//     stderr 逐行解析 JSON stats 回调 OnProgress；rcat 路径解析失败时
//     退化为 stdin 写入计数。
//
// 取消：exec.CommandContext 杀掉子进程；ctx.Err() 非 nil 时（取消或超时）
// 不算上传错误，返回 ctx.Err()，由 worker 按取消/中断路径收尾。
// 失败：best-effort `rclone deletefile` 清理远端残件后按 stderr 模式分类；
// 错误信息不含 options 值。
func (s *RcloneSink) Upload(ctx context.Context, dest Destination, spec UploadSpec) error {
	if spec.RemotePath == "" {
		return apperr.New(apperr.CodeInternal, "云盘上传缺少远端路径")
	}
	bin, err := s.bin()
	if err != nil {
		return apperr.Wrap(apperr.CodeCloudUploadFailed, err)
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}

	var args []string
	if spec.FilePath != "" {
		args = []string{"copyto", spec.FilePath, remoteRef(dest.Name, spec.RemotePath)}
	} else {
		if spec.Reader == nil {
			return apperr.New(apperr.CodeInternal, "云盘上传缺少数据源（Reader 与 FilePath 均为空）")
		}
		args = []string{"rcat", remoteRef(dest.Name, spec.RemotePath),
			"--size", strconv.FormatInt(spec.Size, 10)}
	}
	args = append(args, "--use-json-log", "--stats", "1s", "--stats-one-line", "-v")

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), RemoteEnv(dest)...)
	stderr := newStatsCollector(spec.OnProgress)
	cmd.Stderr = stderr
	if spec.FilePath == "" {
		cmd.Stdin = &monotonicReader{r: spec.Reader, gate: stderr} // stats 解析失败的兜底计数
	}
	runErr := cmd.Run()
	if ctx.Err() != nil {
		// 取消/超时：残件清理仍要尽力（AC10），但不作为上传错误归类
		s.deleteRemoteBestEffort(dest, spec.RemotePath)
		return ctx.Err()
	}
	if runErr == nil {
		return nil
	}
	s.deleteRemoteBestEffort(dest, spec.RemotePath)
	return classifyRcloneError(dest, runErr, stderr.tailText())
}

// Ping 用 `rclone lsd <name>: --max-depth 1` 做轻量连通性测试（只读）。
func (s *RcloneSink) Ping(ctx context.Context, dest Destination) error {
	bin, err := s.bin()
	if err != nil {
		return apperr.Wrap(apperr.CodeCloudUploadFailed, err)
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}
	cmd := exec.CommandContext(ctx, bin,
		"lsd", remoteRef(dest.Name, ""), "--max-depth", "1", "--use-json-log", "-v")
	cmd.Env = append(os.Environ(), RemoteEnv(dest)...)
	stderr := newStatsCollector(nil)
	cmd.Stderr = stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	return classifyRcloneError(dest, err, stderr.tailText())
}

// ---- 已上传核验（同链接重复提交的远端存在性检查） ----

// groupRemotePaths 把完整远端路径按父目录分组（文件名集合），父目录为根时
// 键是空串。核验时每个目录只需一次列举，控制对网盘的请求数。
func groupRemotePaths(paths []string) map[string][]string {
	groups := make(map[string][]string, len(paths))
	for _, p := range paths {
		dir, name := "", p
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			dir, name = p[:i], p[i+1:]
		}
		if name == "" {
			continue
		}
		groups[dir] = append(groups[dir], name)
	}
	return groups
}

// remoteListingEntry 是 lsjson 输出条目的最小解析形态（只取文件名）。
type remoteListingEntry struct {
	Name string `json:"Name"`
}

// notFoundPatterns 是"路径确定不存在"的 stderr 模式（小写匹配）：命中按
// (false, nil) 处理——误判为缺失只是触发重传（安全方向），把真实缺失误判
// 为核验失败会让用户陷入永远失败的重试循环（危险方向）。
var notFoundPatterns = []string{"not found", "couldn't find", "can't find", "does not exist"}

// VerifyUploaded 按父目录分组执行 `rclone lsjson --files-only <name>:<dir>`，
// 比对文件名成员。目录列举失败时按 stderr 模式区分"确定不存在"与核验失败
// （网络/限流等，经 classifyRcloneError 归类返回）。
func (s *RcloneSink) VerifyUploaded(ctx context.Context, dest Destination, paths []string) (bool, error) {
	if len(paths) == 0 {
		return false, apperr.New(apperr.CodeInternal, "云盘核验缺少远端路径")
	}
	bin, err := s.bin()
	if err != nil {
		return false, apperr.Wrap(apperr.CodeCloudUploadFailed, err)
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}
	groups := groupRemotePaths(paths)
	dirs := make([]string, 0, len(groups))
	for dir := range groups {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs) // 核验顺序确定，便于测试与日志排查
	for _, dir := range dirs {
		present, err := s.dirHasAllFiles(ctx, bin, dest, dir, groups[dir])
		if err != nil {
			return false, err
		}
		if !present {
			return false, nil
		}
	}
	return true, nil
}

// dirHasAllFiles 列举 dir（只读）并检查 names 是否全部在场。目录确定不存在
// 返回 (false, nil)；列举成功但成员缺失同样返回 (false, nil)。
func (s *RcloneSink) dirHasAllFiles(ctx context.Context, bin string, dest Destination,
	dir string, names []string) (bool, error) {
	cmd := exec.CommandContext(ctx, bin, "lsjson", remoteRef(dest.Name, dir), "--files-only")
	cmd.Env = append(os.Environ(), RemoteEnv(dest)...)
	var stdout bytes.Buffer
	stderr := newStatsCollector(nil)
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if runErr != nil {
		tail := stderr.tailText()
		if containsAny(strings.ToLower(tail), notFoundPatterns...) {
			return false, nil
		}
		return false, classifyRcloneError(dest, runErr, tail)
	}
	body := strings.TrimSpace(stdout.String())
	present := make(map[string]struct{}, len(names))
	if body != "" {
		var entries []remoteListingEntry
		if err := json.Unmarshal([]byte(body), &entries); err != nil {
			return false, apperr.Wrap(apperr.CodeCloudUploadFailed,
				fmt.Errorf("解析 lsjson 输出失败: %w", err))
		}
		for _, e := range entries {
			present[e.Name] = struct{}{}
		}
	}
	for _, n := range names {
		if _, ok := present[n]; !ok {
			return false, nil
		}
	}
	return true, nil
}

// remoteRef 拼接 rclone 远程引用 `<name>:<path>`。
func remoteRef(name, path string) string {
	return name + ":" + path
}

// deleteRemoteBestEffort 尽力删除远端残件（失败只记 Debug，不阻塞收尾）。
// 使用剥离取消信号的独立时间窗：上传因取消/超时失败时原 ctx 已死。
func (s *RcloneSink) deleteRemoteBestEffort(dest Destination, remotePath string) {
	bin, err := s.bin()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "deletefile", remoteRef(dest.Name, remotePath))
	cmd.Env = append(os.Environ(), RemoteEnv(dest)...)
	if err := cmd.Run(); err != nil {
		s.log().Debug("网盘残件清理失败（尽力而为）", "destination", dest.Name, "error", err.Error())
	}
}

func (s *RcloneSink) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

// ---- stderr 逐行处理：stats JSON 解析 + 尾部留存 + 进度单调门 ----

// statsCollector 是 rclone stderr 的行处理器：先按行缓冲（管道分片到达
// 时不拆散 JSON 行），再逐行尝试解析 JSON stats 取已传字节回调 OnProgress
// （绝对值，字段名容错，research ⚑4），同时保留尾部若干行供错误分类与
// 排障。它同时充当进度"单调门"：stats 解析与 stdin 兜底计数（不同
// goroutine）共用 report，OnProgress 只前进不回退，并发安全。
type statsCollector struct {
	on     func(sent int64)
	mu     sync.Mutex
	buf    []byte
	tail   []string
	last   int64 // 已回调的最大字节（保证单调不减）
	hasVal bool
}

func newStatsCollector(on func(int64)) *statsCollector {
	return &statsCollector{on: on}
}

func (c *statsCollector) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.buf = append(c.buf, p...)
	var lines []string
	for {
		idx := bytes.IndexByte(c.buf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(string(c.buf[:idx]), "\r")
		c.buf = c.buf[idx+1:]
		if line == "" {
			continue
		}
		lines = append(lines, line)
		c.tail = append(c.tail, line)
		if over := len(c.tail) - statsTailLines; over > 0 {
			c.tail = c.tail[over:]
		}
	}
	c.mu.Unlock()
	for _, line := range lines {
		if n, ok := parseStatsBytes(line); ok {
			c.report(n)
		}
	}
	return len(p), nil
}

// report 上报已传字节（绝对值）：两个进度来源（stats JSON / stdin 计数）
// 交错时只前进不回退；on 为 nil 时是纯尾部留存形态。
func (c *statsCollector) report(n int64) {
	if c.on == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hasVal && n <= c.last {
		return
	}
	c.last, c.hasVal = n, true
	c.on(n)
}

func (c *statsCollector) tailText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.tail, "\n")
}

// jsonStatsLine 容错解析 rclone --use-json-log 的 stats 行：
// 优先 stats.bytes，其次 stats.bytes_transferred，再次顶层 bytes
// （不同版本字段有差异，research ⚑4）。
type jsonStatsLine struct {
	Msg   string `json:"msg"`
	Stats *struct {
		Bytes     *int64 `json:"bytes"`
		BytesXfer *int64 `json:"bytes_transferred"`
	} `json:"stats"`
	Bytes *int64 `json:"bytes"`
}

func parseStatsBytes(line string) (int64, bool) {
	if !bytes.HasPrefix(bytes.TrimSpace([]byte(line)), []byte("{")) {
		return 0, false
	}
	var jl jsonStatsLine
	if err := json.Unmarshal([]byte(line), &jl); err != nil {
		return 0, false
	}
	if jl.Stats != nil {
		if jl.Stats.Bytes != nil {
			return *jl.Stats.Bytes, true
		}
		if jl.Stats.BytesXfer != nil {
			return *jl.Stats.BytesXfer, true
		}
	}
	if jl.Msg == "stats" && jl.Bytes != nil {
		return *jl.Bytes, true
	}
	return 0, false
}

// monotonicReader 包装 rcat 的 stdin 数据源：被 rclone 读走的字节数
// 累计为绝对值，经 statsCollector 的单调门回调（stats 解析失败时的
// 兜底进度）。
type monotonicReader struct {
	r    io.Reader
	gate *statsCollector
	sent int64
}

func (m *monotonicReader) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	if n > 0 {
		m.sent += int64(n)
		m.gate.report(m.sent)
	}
	return n, err
}

// ---- 错误分类（stderr 模式 + 退出码 → apperr 新码） ----

// classifyRcloneError 按退出码 + stderr 模式表把 rclone 失败归类为
// CLOUD_* 错误码。tail 经 dest 的 options 值脱敏后进入
// AppError.Message（仅内部日志可见，用户只看 UserText）。
func classifyRcloneError(dest Destination, runErr error, tail string) *apperr.AppError {
	text := strings.ToLower(tail)
	var code apperr.Code = apperr.CodeCloudUploadFailed
	switch {
	case containsAny(text,
		"user or password", "password is incorrect", "invalid password",
		"login failed", "authentication", "unauthorized", "unauthorised",
		"invalid_grant", "401", "forbidden", "403", "access denied"):
		code = apperr.CodeCloudAuthFailed
	case containsAny(text,
		"quota", "storage full", "not enough space", "insufficient storage",
		"out of space"):
		code = apperr.CodeCloudQuota
	case containsAny(text,
		"dial tcp", "connection refused", "connection reset", "no route to host",
		"network is unreachable", "i/o timeout", "timed out", "timeout",
		"no such host", "name resolution", "temporary failure",
		"too many requests", "429", "server is offline"):
		code = apperr.CodeCloudNetwork
	}
	var ee *exec.ExitError
	exitCode := 0
	if errors.As(runErr, &ee) {
		exitCode = ee.ExitCode()
	}
	msg := fmt.Sprintf("rclone 退出码 %d: %s", exitCode, redactTail(dest, tail))
	return apperr.Wrap(code, errors.New(msg))
}

func containsAny(text string, patterns ...string) bool {
	for _, p := range patterns {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

// redactValueMark 是脱敏替换占位。
const redactValueMark = "***"

// redactTail 把 stderr 尾部中出现的 options 值（长度 ≥4，避免误伤普通
// 短词）替换为 ***，并整体截断到 1KB——错误原文只进结构化日志，
// 绝不携带凭据（数据红线）。
func redactTail(dest Destination, tail string) string {
	t := tail
	for _, v := range dest.Options {
		if len(v) < 4 {
			continue
		}
		t = strings.ReplaceAll(t, v, redactValueMark)
	}
	if len(t) > 1024 {
		t = t[:1024] + "...(截断)"
	}
	return t
}
