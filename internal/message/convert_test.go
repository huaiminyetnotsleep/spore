package message

import (
	"testing"

	"github.com/gotd/td/tg"
)

// 源媒体测试样例：字段值唯一，便于断言 Location 与源对象一致。
const (
	testMediaID      = int64(42)
	testAccessHash   = int64(987654321)
	testDC           = 2
	testPhotoSizeBig = 640
)

func testPhoto() *tg.MessageMediaPhoto {
	return &tg.MessageMediaPhoto{Photo: &tg.Photo{
		ID:            testMediaID,
		AccessHash:    testAccessHash,
		DCID:          testDC,
		FileReference: []byte{0xAB, 0xCD, 0xEF},
		Sizes: []tg.PhotoSizeClass{
			// 小尺寸在前：断言下载位置选中最大的 "i" 尺寸
			&tg.PhotoSize{Type: "x", Size: 200},
			&tg.PhotoSize{Type: "i", Size: testPhotoSizeBig},
		},
	}}
}

func testDocument(attrs ...tg.DocumentAttributeClass) *tg.MessageMediaDocument {
	return testDocumentMime("application/octet-stream", attrs...)
}

func testDocumentMime(mime string, attrs ...tg.DocumentAttributeClass) *tg.MessageMediaDocument {
	return &tg.MessageMediaDocument{Document: &tg.Document{
		ID:            testMediaID,
		AccessHash:    testAccessHash,
		DCID:          testDC,
		FileReference: []byte{0xAB, 0xCD, 0xEF},
		MimeType:      mime,
		Size:          1024,
		Attributes:    attrs,
	}}
}

// assertLocationSource 断言引用直发所需的媒体坐标完整保留在 Location 中。
func assertLocationSource(t *testing.T, loc tg.InputFileLocationClass) {
	t.Helper()
	switch l := loc.(type) {
	case *tg.InputPhotoFileLocation:
		if l.ID != testMediaID || l.AccessHash != testAccessHash {
			t.Errorf("photo 坐标与源对象不一致: id=%d hash=%d", l.ID, l.AccessHash)
		}
	case *tg.InputDocumentFileLocation:
		if l.ID != testMediaID || l.AccessHash != testAccessHash {
			t.Errorf("document 坐标与源对象不一致: id=%d hash=%d", l.ID, l.AccessHash)
		}
	default:
		t.Errorf("Location 形态不支持: %T", loc)
	}
}

func TestConvertOnePhoto(t *testing.T) {
	it := ConvertOne(&tg.Message{ID: 7, Media: testPhoto()})
	m := it.Media
	if m == nil || m.Kind != KindPhoto {
		t.Fatalf("应转换为 KindPhoto: %+v", m)
	}
	if m.DCID != testDC {
		t.Fatalf("应保留 photo DCID=%d，得到 %d", testDC, m.DCID)
	}
	loc, ok := m.Location.(*tg.InputPhotoFileLocation)
	if !ok || loc.ThumbSize != "i" {
		t.Fatalf("Location 应保持最大尺寸 i 的下载位置: %+v", m.Location)
	}
	assertLocationSource(t, m.Location)
}

func TestConvertOneDocumentKinds(t *testing.T) {
	cases := []struct {
		name     string
		attrs    []tg.DocumentAttributeClass
		wantKind ItemKind
	}{
		{
			name:     "视频",
			attrs:    []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{W: 1280, H: 720, Duration: 30}},
			wantKind: KindVideo,
		},
		{
			name:     "音乐",
			attrs:    []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Title: "song", Duration: 180}},
			wantKind: KindAudio,
		},
		{
			name:     "语音",
			attrs:    []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true, Duration: 12}},
			wantKind: KindVoice,
		},
		{
			name:     "普通文件",
			attrs:    nil,
			wantKind: KindDocument,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mm := testDocument(tc.attrs...)
			it := ConvertOne(&tg.Message{ID: 7, Media: mm})
			m := it.Media
			if m == nil || m.Kind != tc.wantKind {
				t.Fatalf("Kind 应为 %v: %+v", tc.wantKind, m)
			}
			if m.DCID != testDC {
				t.Errorf("应保留 document DCID=%d，得到 %d", testDC, m.DCID)
			}
			if _, ok := m.Location.(*tg.InputDocumentFileLocation); !ok {
				t.Errorf("Location 应保持不变: %+v", m.Location)
			}
			assertLocationSource(t, m.Location)
		})
	}
}

// 排除类型：贴纸 / GIF / 兜底类型的转换语义。
func TestConvertOneExclusions(t *testing.T) {
	t.Run("贴纸", func(t *testing.T) {
		it := ConvertOne(&tg.Message{ID: 7, Media: testDocument(
			&tg.DocumentAttributeSticker{})})
		if it.Media == nil || it.Media.Kind != KindUnsupported {
			t.Fatalf("贴纸应为 KindUnsupported: %+v", it.Media)
		}
	})

	t.Run("GIF 动图", func(t *testing.T) {
		it := ConvertOne(&tg.Message{ID: 7, Media: testDocumentMime("video/mp4",
			&tg.DocumentAttributeAnimated{},
			&tg.DocumentAttributeFilename{FileName: "cat.gif"},
		)})
		m := it.Media
		if m == nil || m.Kind != KindVideo {
			t.Fatalf("GIF 现路径按 video 处理: %+v", m)
		}
		if m.Location == nil {
			t.Error("GIF 仍应保留下载位置")
		}
	})

	t.Run("不支持类型", func(t *testing.T) {
		it := ConvertOne(&tg.Message{ID: 7, Media: &tg.MessageMediaContact{}})
		if it.Media == nil || it.Media.Kind != KindUnsupported {
			t.Errorf("兜底类型应为 KindUnsupported: %+v", it.Media)
		}
	})
}

// 视频源缩略图坐标提取：最大 JPEG 档、跳过 stripped/path 预览、
// 非视频类型不提取。
func TestConvertOneVideoThumb(t *testing.T) {
	videoDoc := func(thumbs []tg.PhotoSizeClass) *tg.MessageMediaDocument {
		mm := testDocument(&tg.DocumentAttributeVideo{W: 1280, H: 720, Duration: 30})
		mm.Document.(*tg.Document).Thumbs = thumbs
		return mm
	}

	t.Run("取最大档并带 ThumbSize", func(t *testing.T) {
		mm := videoDoc([]tg.PhotoSizeClass{
			&tg.PhotoSize{Type: "s", W: 90, H: 90, Size: 1200},
			&tg.PhotoSize{Type: "m", W: 320, H: 180, Size: 8600},
		})
		m := ConvertOne(&tg.Message{ID: 7, Media: mm}).Media
		if m.Thumb == nil {
			t.Fatalf("视频应携带缩略图坐标: %+v", m)
		}
		loc, ok := m.Thumb.Location.(*tg.InputDocumentFileLocation)
		if !ok {
			t.Fatalf("缩略图位置应为 InputDocumentFileLocation: %T", m.Thumb.Location)
		}
		if loc.ThumbSize != "m" || m.Thumb.Size != 8600 {
			t.Errorf("应取最大档 m/8600，得到 %s/%d", loc.ThumbSize, m.Thumb.Size)
		}
		assertLocationSource(t, loc)
	})

	t.Run("跳过 stripped 与 path 档", func(t *testing.T) {
		mm := videoDoc([]tg.PhotoSizeClass{
			&tg.PhotoStrippedSize{Type: "i", Bytes: []byte{1, 2, 3}},
			&tg.PhotoPathSize{Type: "j", Bytes: []byte{4, 5}},
		})
		m := ConvertOne(&tg.Message{ID: 7, Media: mm}).Media
		if m.Thumb != nil {
			t.Errorf("stripped/path 档不构成封面候选: %+v", m.Thumb)
		}
	})

	t.Run("无缩略图", func(t *testing.T) {
		m := ConvertOne(&tg.Message{ID: 7, Media: videoDoc(nil)}).Media
		if m.Thumb != nil {
			t.Errorf("无缩略图时 Thumb 应为 nil: %+v", m.Thumb)
		}
	})

	t.Run("非视频类型不提取", func(t *testing.T) {
		mm := testDocument(&tg.DocumentAttributeFilename{FileName: "f.bin"})
		mm.Document.(*tg.Document).Thumbs = []tg.PhotoSizeClass{
			&tg.PhotoSize{Type: "m", W: 320, H: 320, Size: 8600},
		}
		m := ConvertOne(&tg.Message{ID: 7, Media: mm}).Media
		if m.Kind != KindDocument {
			t.Fatalf("应为 KindDocument: %+v", m)
		}
		if m.Thumb != nil {
			t.Errorf("普通文档不提取缩略图坐标: %+v", m.Thumb)
		}
	})
}
