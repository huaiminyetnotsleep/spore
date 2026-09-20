package media

import "encoding/binary"

// SniffHeadBytes 是容器判定的头部缓冲大小：覆盖 MP4 顶层 box 头链的前几个
// box（ftyp 通常 <1KB，free/skip 前导亦小），只需读到首个 moov/mdat 的
// 8/16 字节头即可判定，不需要读整个 moov box。
const SniffHeadBytes = 64 << 10

// MoovPosition 描述 MP4 顶层 box 链中 moov 与 mdat 的相对位置，决定超大
// 视频的切段模式。
type MoovPosition int

const (
	// MoovUnknown 表示非 MP4 容器（无 ftyp，如 MKV/WebM）或头部缓冲内
	// 无法判定——保守回退完整落盘路径（确定性预判，非试错）。
	MoovUnknown MoovPosition = iota
	// MoovHead 表示 moov 在 mdat 之前：demuxer 顺序读即可解析，支持
	// 管道输入的单遍流式切段（源不落盘）。
	MoovHead
	// MoovTail 表示 mdat 在 moov 之前：demuxer 必须读到文件尾才能取得
	// 索引，管道流不可行——回退完整落盘后按区间切段。
	MoovTail
)

func (p MoovPosition) String() string {
	switch p {
	case MoovHead:
		return "head"
	case MoovTail:
		return "tail"
	default:
		return "unknown"
	}
}

// SniffMoovPosition 解析 head 缓冲内的 MP4 顶层 box 头链（size+type，含
// size==1 的 largesize 扩展与 size==0 的延伸至文件尾），判定 moov 与 mdat
// 谁先出现：ftyp 之后首个 moov → MoovHead；首个 mdat → MoovTail；无 ftyp、
// box 链损坏或缓冲内未见 moov/mdat → MoovUnknown（保守回退，流式路径只对
// 确认头 moov 的 MP4 开放）。判定只消费调用方传入的缓冲副本语义——head
// 本身不被修改，消费流由调用方经 io.MultiReader 接回。
func SniffMoovPosition(head []byte) MoovPosition {
	sawFTYP := false
	off := 0
	for off+8 <= len(head) {
		size := int64(binary.BigEndian.Uint32(head[off : off+4]))
		typ := string(head[off+4 : off+8])
		hdrLen := int64(8)
		switch size {
		case 0: // box 延伸至文件尾：按缓冲剩余推进（仅影响游走，不影响判定）
			size = int64(len(head)) - int64(off)
		case 1: // 64 位 largesize 扩展
			if off+16 > len(head) {
				return MoovUnknown
			}
			size = int64(binary.BigEndian.Uint64(head[off+8 : off+16]))
			hdrLen = 16
		}
		if size < hdrLen || size < 0 {
			return MoovUnknown // 损坏的 box 链（含 int64 溢出）
		}
		switch typ {
		case "ftyp":
			sawFTYP = true
		case "moov":
			if sawFTYP {
				return MoovHead
			}
			return MoovUnknown // ftyp 之前出现 moov：非规范 MP4，保守回退
		case "mdat":
			if sawFTYP {
				return MoovTail
			}
			return MoovUnknown
		}
		off += int(size)
	}
	return MoovUnknown // 头部缓冲内未见 moov/mdat（或非 MP4 容器）
}
