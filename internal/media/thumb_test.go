package media

// ExtractFrameAtJPEG 的守卫测试：ffmpeg 缺失时跳过（与运行时的尽力而为
// 降级同语义）；存在时用 ffmpeg 现场生成 2 秒测试视频，断言指定时间点
// 能抽出 JPEG 帧——覆盖 -ss 定位参数与完整文件输入路径（区别于头部字节
// 的 ExtractFrameJPEG）。

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExtractFrameAtJPEG(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg 不可用，跳过定位抽帧测试")
	}
	dir := t.TempDir()
	video := filepath.Join(dir, "testsrc.mp4")
	gen := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-pix_fmt", "yuv420p", video)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("生成测试视频失败: %v（%.200s）", err, out)
	}

	jpeg, err := ExtractFrameAtJPEG(context.Background(), ffmpeg, video, 0.5)
	if err != nil {
		t.Fatalf("定位抽帧应成功: %v", err)
	}
	if len(jpeg) == 0 {
		t.Fatal("抽帧应输出非空 JPEG")
	}
	// JPEG 魔数：FFD8FF（与 mjpeg 管道输出一致）
	if !bytes.HasPrefix(jpeg, []byte{0xFF, 0xD8, 0xFF}) {
		t.Fatalf("输出应为 JPEG 字节流: % x", jpeg[:min(3, len(jpeg))])
	}
	if _, err := os.Stat(video); err != nil {
		t.Fatalf("源视频不应被抽帧改动: %v", err)
	}
}

func TestExtractFrameAtJPEGFailsOnMissingFile(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg 不可用，跳过定位抽帧测试")
	}
	if _, err := ExtractFrameAtJPEG(context.Background(), "ffmpeg", "no-such-file.mp4", 0); err == nil {
		t.Fatal("源文件不存在应返回错误")
	}
}
