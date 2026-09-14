// 占位消息的实时进度编辑：worker 把 botapi 发出的"正在获取消息..."占位
// 提示定期编辑为下载/上传两行进度。编辑频率与失败姿态都按 Bot API 限频
// 约束设计（每聊天约 1 msg/s 的经验红线，编辑与发送共享预算）：
//   - 5 秒 tick + 内容不变跳过，把频率压到远低于红线；
//   - 编辑失败即熔断本任务后续编辑（占位可能被用户手动删除，属常态），
//     绝不反复撞墙，也不影响任务主流程。

package queue

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// StatusPromptHTML 是占位提示的初始文案（botapi 发送与 worker 编辑共用
// 同一来源；常量定义在本包——queue 不依赖 botapi，而 botapi 已依赖 queue）。
const StatusPromptHTML = "正在获取消息..."

// StatusCloudPromptHTML 是云盘下载任务（/download）占位提示的初始文案：
// 语义区别于普通任务，进度编辑同样以它为前缀（job 感知，见 startProgressEditor）。
const StatusCloudPromptHTML = "正在获取消息并下载到网盘..."

// StatusCancelledHTML 是任务被取消后占位消息的终态文案（worker 收尾与装配层
// 的排队取消钩子共用）。取消不删除占位消息，让用户在聊天里看到明确结果。
const StatusCancelledHTML = "任务已取消。"

// progressEditInterval 是占位消息进度编辑的 tick 间隔。
// 5 秒 ≈ 0.2 次/秒/聊天；同用户两个并发任务也仅 0.4 次/秒。
const progressEditInterval = 5 * time.Second

// startProgressEditor 启动占位消息的实时进度编辑，返回 stop 函数（任务
// 收尾必须调用）。无占位消息（StatusMsgID==0）或无持久化记录（RequestID==0，
// 进度无处可读）时返回 no-op——占位文案保持初始状态。
func startProgressEditor(ctx context.Context, d Deps, j Job) func() {
	return startProgressEditorWithInterval(ctx, d, j, progressEditInterval)
}

// startProgressEditorWithInterval 是可注入间隔的实现形态（测试用短间隔）。
func startProgressEditorWithInterval(ctx context.Context, d Deps, j Job, interval time.Duration) func() {
	if j.RequestID == 0 || j.StatusMsgID == 0 || d.Progress == nil {
		return func() {}
	}
	// 占位前缀 job 感知：云盘任务用云盘文案，进度编辑不把占位改回普通任务措辞
	prompt := StatusPromptHTML
	if j.CloudDest != "" {
		prompt = StatusCloudPromptHTML
	}
	stop := make(chan struct{})
	var stopOnce sync.Once
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		last := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
			}
			if last == "\x00" { // 已熔断：本任务不再编辑
				continue
			}
			snap := d.Progress.Snapshot(j.RequestID)
			if snap == nil || snap.TotalBytes <= 0 {
				continue // 文本任务或媒体大小尚未登记：无可展示的进度
			}
			html := renderProgressStatus(prompt, snap.TotalBytes, snap.DownloadedBytes, snap.UploadedBytes)
			if html == last {
				continue // 内容不变：跳过编辑，省一次 Bot API 调用
			}
			if err := d.Sender.EditMessageText(ctx, j.ChatID, j.StatusMsgID, html); err != nil {
				d.Log.Debug("占位消息进度编辑失败，熔断本任务后续编辑",
					"job_id", j.ID, "request_id", j.RequestID, "error", err.Error())
				last = "\x00" // 熔断标记：占位可能已被删除或持续限流
				continue
			}
			last = html
		}
	}()
	return func() { stopOnce.Do(func() { close(stop) }); <-done }
}

// renderProgressStatus 渲染占位消息的进度 HTML：保留初始文案前缀（普通任务与
// 云盘任务各异），下载与上传两行独立展示（重叠管线）。内容全部由服务端受控
// 生成，不含文件名等用户可控片段，无 HTML 转义面。
func renderProgressStatus(prompt string, total, downloaded, uploaded int64) string {
	return fmt.Sprintf("%s\n⬇️ 下载 %s（%s / %s）\n⬆️ 上传 %s（%s / %s）",
		prompt,
		percentText(downloaded, total), bytesText(downloaded), bytesText(total),
		percentText(uploaded, total), bytesText(uploaded), bytesText(total))
}

// percentText 渲染百分比；总量未知或异常溢出按 0% 展示，封顶 100%。
func percentText(bytes, total int64) string {
	if total <= 0 || bytes <= 0 {
		return "0%"
	}
	p := bytes * 100 / total
	if p > 100 {
		p = 100
	}
	return fmt.Sprintf("%d%%", p)
}

// bytesText 字节数 → 人类可读形式（与前端 shared/format.ts fmtBytes 同源：
// GiB/MiB/KiB 两位小数去尾零，小字节数原样 B）。
func bytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return trimZeros(fmt.Sprintf("%.2f", float64(n)/(1<<30))) + " GiB"
	case n >= 1<<20:
		return trimZeros(fmt.Sprintf("%.2f", float64(n)/(1<<20))) + " MiB"
	case n >= 1<<10:
		return trimZeros(fmt.Sprintf("%.2f", float64(n)/(1<<10))) + " KiB"
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// trimZeros 去掉小数尾零与孤立小数点（2.50 → 2.5，1.00 → 1）。
func trimZeros(s string) string {
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}
