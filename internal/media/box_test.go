package media

import (
	"encoding/binary"
	"testing"
)

// appendBox 向 buf 追加一个顶层 box：4 字节大端 size（含头）+ type + payload。
func appendBox(buf []byte, typ string, payload []byte) []byte {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(8+len(payload)))
	buf = append(buf, size[:]...)
	buf = append(buf, typ...)
	return append(buf, payload...)
}

// 头 moov：ftyp → moov → mdat，判定应为 MoovHead（moov 头部即足以判定，
// 不需要完整 moov 内容）。
func TestSniffMoovHead(t *testing.T) {
	var buf []byte
	buf = appendBox(buf, "ftyp", []byte("isom\x00\x00\x02\x00isomiso2"))
	buf = appendBox(buf, "moov", make([]byte, 64)) // 截断的 moov：只含头部即可
	buf = appendBox(buf, "mdat", nil)
	if got := SniffMoovPosition(buf); got != MoovHead {
		t.Fatalf("ftyp→moov→mdat 应判 MoovHead，得到 %v", got)
	}
}

// 尾 moov：ftyp → mdat → moov，判定应为 MoovTail（mdat 头部即足以判定）。
func TestSniffMoovTail(t *testing.T) {
	var buf []byte
	buf = appendBox(buf, "ftyp", []byte("isom\x00\x00\x02\x00isomiso2"))
	var mdat [4]byte
	binary.BigEndian.PutUint32(mdat[:], 1<<30) // 声明 1GB mdat（内容截断，头即可）
	buf = append(buf, mdat[:]...)
	buf = append(buf, "mdat"...)
	buf = appendBox(buf, "moov", make([]byte, 16))
	if got := SniffMoovPosition(buf); got != MoovTail {
		t.Fatalf("ftyp→mdat→moov 应判 MoovTail，得到 %v", got)
	}
}

// free/skip 前导与中间 box 被跳过，不影响判定。
func TestSniffSkipsFreeBoxes(t *testing.T) {
	var buf []byte
	buf = appendBox(buf, "free", make([]byte, 32))
	buf = appendBox(buf, "ftyp", []byte("isom"))
	buf = appendBox(buf, "skip", make([]byte, 8))
	buf = appendBox(buf, "mdat", nil)
	if got := SniffMoovPosition(buf); got != MoovTail {
		t.Fatalf("free→ftyp→skip→mdat 应判 MoovTail，得到 %v", got)
	}
}

// largesize 扩展（size==1）：64 位长度头的 moov 照常判定。
func TestSniffLargeSizeBox(t *testing.T) {
	var buf []byte
	buf = appendBox(buf, "ftyp", []byte("isom"))
	buf = append(buf, 0, 0, 0, 1) // size==1 → largesize
	buf = append(buf, "moov"...)
	var large [8]byte
	binary.BigEndian.PutUint64(large[:], 1<<40) // 1TB 声明（防御性值）
	buf = append(buf, large[:]...)
	if got := SniffMoovPosition(buf); got != MoovHead {
		t.Fatalf("largesize moov 应判 MoovHead，得到 %v", got)
	}
}

// 非 MP4 容器（EBML/WebM、随机字节、空缓冲）与无 ftyp 的 box 链一律
// MoovUnknown（保守回退，流式路径只对确认头 moov 的 MP4 开放）。
func TestSniffNonMP4AndMalformed(t *testing.T) {
	cases := [][]byte{
		{0x1A, 0x45, 0xDF, 0xA3, 0x9F, 0xB2, 0x86, 0x01, 0x42, 0x85}, // EBML（WebM/MKV）
		{0xDE, 0xAD, 0xBE, 0xEF, 0xBA, 0xAD, 0xF0, 0x0D},
		nil,
	}
	for _, c := range cases {
		if got := SniffMoovPosition(c); got != MoovUnknown {
			t.Fatalf("非 MP4 输入 %v 应判 MoovUnknown，得到 %v", c, got)
		}
	}

	var noFtyp []byte
	noFtyp = appendBox(noFtyp, "moov", nil) // moov 前置于 ftyp：非规范
	if got := SniffMoovPosition(noFtyp); got != MoovUnknown {
		t.Fatalf("无 ftyp 的 moov 应保守判 MoovUnknown，得到 %v", got)
	}

	// 损坏 size（< 头长）→ unknown
	var bad []byte
	bad = appendBox(bad, "ftyp", []byte("isom"))
	bad = append(bad, 0, 0, 0, 4) // size=4 < 8
	bad = append(bad, "mdat"...)
	if got := SniffMoovPosition(bad); got != MoovUnknown {
		t.Fatalf("损坏 box 链应判 MoovUnknown，得到 %v", got)
	}
}

// 头部缓冲内只有 ftyp（moov/mdat 头都未出现，如 ftyp 后紧跟超大自定义
// box）→ MoovUnknown。
func TestSniffUndetermined(t *testing.T) {
	var buf []byte
	buf = appendBox(buf, "ftyp", []byte("isom"))
	buf = appendBox(buf, "uuid", make([]byte, SniffHeadBytes)) // 巨大自定义 box 吞掉缓冲
	if got := SniffMoovPosition(buf[:SniffHeadBytes]); got != MoovUnknown {
		t.Fatalf("缓冲内无法判定应返回 MoovUnknown，得到 %v", got)
	}
}
