// 视频封面兜底：源文档无自带缩略图时，用 ffmpeg 从视频头部字节提取首帧
// JPEG 作为重发封面。头部字节由调用方（queue 的缩略图解析）从主下载流
// 顺序读出后传入，本文件只负责与 ffmpeg 进程的交互。

package media

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// ffmpegTimeout 是单次抽帧的进程级上限：正常抽帧在秒级完成，超时视为
// 该视频无法抽帧（调用方降级为无封面）。
const ffmpegTimeout = 30 * time.Second

// thumbMaxDimension 是缩略图的最长边像素（与 Telegram 客户端为文档生成
// 封面的规格一致），输出按原宽高比缩到该框内。
const thumbMaxDimension = 320

// ExtractFrameJPEG 用 ffmpeg 从视频头部字节提取首帧 JPEG（缩略图兜底）。
// head 需包含容器头与首帧数据——moov 在前的 MP4 数 MB 足够；moov 在尾部
// 的视频无法从头解析，ffmpeg 报错返回，调用方降级为无封面。
// ffmpeg 不可用（未安装/路径不对）时返回错误，与抽帧失败同语义。
func ExtractFrameJPEG(ctx context.Context, ffmpegPath, tmpDir string, head []byte) ([]byte, error) {
	// 经临时文件喂入而非 stdin 管道：MP4 demuxer 解析 moov 需要可回退的
	// seekable 输入，非 seekable 管道对部分正常文件也会失败
	f, err := os.CreateTemp(tmpDir, "thumb-head-*.bin")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(head); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return runFFmpegFrame(ctx, ffmpegPath,
		"-i", f.Name(),
		"-frames:v", "1",
		"-an", "-sn", "-dn",
	)
}

// ExtractFrameAtJPEG 用 ffmpeg 从完整视频文件的指定时间点提取单帧 JPEG。
// 分卷拆投的逐段封面用：分段字节流没有容器头不可解码，改为按"段起始
// 字节 / 总大小 × 总时长"换算时间戳，对已完整落盘的文件用 -ss 输入快速
// 定位抽帧（定位到目标时间最近的解码关键帧）。VBR 视频按平均码率近似，
// 画面与段边界可能有小偏移；文件不完整或容器无法解析时返回错误，调用方
// 降级为无封面。
func ExtractFrameAtJPEG(ctx context.Context, ffmpegPath, videoPath string, sec float64) ([]byte, error) {
	return runFFmpegFrame(ctx, ffmpegPath,
		"-ss", strconv.FormatFloat(sec, 'f', 3, 64),
		"-i", videoPath,
		"-frames:v", "1",
		"-an", "-sn", "-dn",
	)
}

// runFFmpegFrame 执行 ffmpeg 单帧抽取并把 stdout 收为 JPEG 字节；输出帧
// 公共参数（缩放框、质量、mjpeg 管道输出）在此统一，args 为输入侧参数
// （-i 之前与之后的部分）。
func runFFmpegFrame(ctx context.Context, ffmpegPath string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()

	full := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, args...)
	full = append(full,
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", thumbMaxDimension, thumbMaxDimension),
		"-q:v", "5",
		"-f", "mjpeg", "pipe:1",
	)
	cmd := exec.CommandContext(ctx, ffmpegPath, full...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg 抽帧失败: %w（stderr: %.300s）", err, stderr.String())
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("ffmpeg 未输出帧数据（stderr: %.300s）", stderr.String())
	}
	return out.Bytes(), nil
}
