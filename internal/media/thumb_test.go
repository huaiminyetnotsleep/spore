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

// TestExtractSegmentCoverJPEG 验证分段封面落在段内切点关键帧上：open-GOP
// 源在恢复点切出的段，段首显示序最前的帧是参考缺失的前导 B 帧（解码为
// 绿红条纹花屏），封面必须与其无关、与全上下文同切点抽帧字节一致（同一
// ffmpeg 下两条路径解码同一个 I 帧，输出确定一致）。编码器不支持 open_gop
// 时跳过（无法构造坏前缀分段）。
func TestExtractSegmentCoverJPEG(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg 不可用，跳过分段封面测试")
	}
	dir := t.TempDir()
	// open_gop + 固定 B 帧模式：keyint 边界的 I 帧是非 IDR 恢复点，其后
	//（解码序）紧跟显示序在前、参考上一 GOP 的前导 B 帧
	src := filepath.Join(dir, "open_gop.mp4")
	gen := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=4:size=320x240:rate=24",
		"-c:v", "libx264",
		"-x264-params", "open_gop=1:keyint=24:bframes=2:b_adapt=0:scenecut=0",
		"-pix_fmt", "yuv420p", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("无法生成 open-GOP 测试源（编码器不支持）: %v（%.200s）", err, out)
	}
	// 按生产切段命令在恢复点切（split.cutVideoSegment 同款参数）
	seg := filepath.Join(dir, "seg.mkv")
	cut := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", "2.000", "-i", src, "-t", "1.5",
		"-c", "copy", "-avoid_negative_ts", "make_zero",
		"-f", "matroska", "-y", seg)
	if out, err := cut.CombinedOutput(); err != nil {
		t.Fatalf("生成坏前缀分段失败: %v（%.200s）", err, out)
	}

	cover, err := ExtractSegmentCoverJPEG(context.Background(), ffmpeg, seg)
	if err != nil {
		t.Fatalf("分段封面抽取应成功: %v", err)
	}
	if !bytes.HasPrefix(cover, []byte{0xFF, 0xD8, 0xFF}) {
		t.Fatalf("封面应为 JPEG 字节流: % x", cover[:min(3, len(cover))])
	}
	ref, err := ExtractFrameAtJPEG(context.Background(), ffmpeg, src, 2.0)
	if err != nil {
		t.Fatalf("全上下文参考抽帧应成功: %v", err)
	}
	if !bytes.Equal(cover, ref) {
		t.Fatal("分段封面应落在切点关键帧上（与全上下文抽帧一致），而非段首坏前缀帧")
	}
	if _, err := os.Stat(seg); err != nil {
		t.Fatalf("分段文件不应被抽帧改动: %v", err)
	}
}

func TestExtractSegmentCoverJPEGFailsOnMissingFile(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg 不可用，跳过分段封面测试")
	}
	if _, err := ExtractSegmentCoverJPEG(context.Background(), "ffmpeg", "no-such-file.mkv"); err == nil {
		t.Fatal("源文件不存在应返回错误（两级抽取都失败）")
	}
}
