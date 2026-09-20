// ffmpeg 能力探测：可播放分段依赖 matroska 封装器——项目自带的精简
// ffmpeg 已包含，但自定义 FFMPEG_PATH 指向阉割构建（如只有 mjpeg muxer
// 的抽帧专用构建）时，切段会在运行期以 "Requested output format 'matroska'
// is not known" 失败并回退字节分段。启动期探测一次即可把这类部署问题
// 提前暴露为明确的告警日志。

package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// muxerProbeTimeout 是 muxer 列表查询的时间上限（进程启动即返回，超时视为
// 异常构建/挂载环境）。
const muxerProbeTimeout = 10 * time.Second

// CheckMatroskaMuxer 探测 ffmpeg 是否包含 matroska 封装器（可播放切段
// 的输出容器）。ffmpeg 不可执行或列表中无 matroska 时返回错误，调用方
// （启动流程）记告警日志——可播放切段将在运行期自动回退字节分段。
func CheckMatroskaMuxer(ctx context.Context, ffmpegPath string) error {
	ctx, cancel := context.WithTimeout(ctx, muxerProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-muxers").CombinedOutput()
	if err != nil {
		return fmt.Errorf("查询 ffmpeg muxer 列表失败: %w（输出: %.200s）", err, out)
	}
	if !bytes.Contains(out, []byte("matroska")) {
		return errors.New("ffmpeg 构建缺少 matroska 封装器（精简/阉割构建），请更换完整版 ffmpeg")
	}
	return nil
}
