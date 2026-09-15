package message

import (
	"slices"
	"strings"

	"github.com/gotd/td/tg"
)

// Convert 将按 ID 升序的源消息转换为内部条目。
// 仅保留可重发的内容类型；服务消息等应在上游被过滤，此处兜底标为 Unsupported。
func Convert(msgs []*tg.Message) []Item {
	items := make([]Item, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}
		items = append(items, ConvertOne(m))
	}
	return items
}

// ConvertOne 转换单条源消息；RefreshMedia 等只需定位一条的场景使用，
// 避免为找一条而转换整组（Clone entities、构造 Media 的开销）。
func ConvertOne(m *tg.Message) Item {
	it := Item{
		ID:       m.ID,
		Text:     m.Message, // Telegram 中正文与 caption 为同一字段
		Entities: slices.Clone(m.Entities),
	}
	if gid, ok := m.GetGroupedID(); ok {
		it.GroupedID = gid
	}

	switch media := m.Media.(type) {
	case nil:
		// 无媒体：网页预览（MessageMediaWebPage）或纯文本——一律按文本处理
	case *tg.MessageMediaPhoto:
		it.Media = extractPhoto(media)
	case *tg.MessageMediaDocument:
		it.Media = extractDocument(media)
	default:
		it.Media = &Media{Kind: KindUnsupported}
	}
	return it
}

// extractPhoto 提取图片最大尺寸并构造下载位置。
func extractPhoto(mm *tg.MessageMediaPhoto) *Media {
	p, ok := mm.Photo.(*tg.Photo)
	if !ok || p == nil {
		return &Media{Kind: KindUnsupported}
	}
	var bestSize int64
	var thumbType string
	found := false
	for _, s := range p.Sizes {
		switch ps := s.(type) {
		case *tg.PhotoSizeProgressive:
			for _, n := range ps.Sizes {
				if int64(n) > bestSize {
					bestSize, thumbType, found = int64(n), ps.Type, true
				}
			}
		case *tg.PhotoSize:
			if int64(ps.Size) > bestSize {
				bestSize, thumbType, found = int64(ps.Size), ps.Type, true
			}
			// PhotoStrippedSize 为内联缩略图，跳过
		}
	}
	if !found {
		return &Media{Kind: KindUnsupported, DCID: p.DCID}
	}
	return &Media{
		Kind:     KindPhoto,
		Location: photoLocation(p, thumbType),
		DCID:     p.DCID,
		FileName: "photo.jpg",
		Size:     bestSize,
	}
}

func photoLocation(p *tg.Photo, thumbType string) tg.InputFileLocationClass {
	return &tg.InputPhotoFileLocation{
		ID:            p.ID,
		AccessHash:    p.AccessHash,
		FileReference: slices.Clone(p.FileReference),
		ThumbSize:     thumbType,
	}
}

// extractDocument 按 attributes 判定 Voice/Audio/Video/Document 并填充元数据。
func extractDocument(mm *tg.MessageMediaDocument) *Media {
	d, ok := mm.Document.(*tg.Document)
	if !ok || d == nil {
		return &Media{Kind: KindUnsupported}
	}
	var videoAttr *tg.DocumentAttributeVideo
	var audioAttr *tg.DocumentAttributeAudio
	var stickerAttr *tg.DocumentAttributeSticker
	fileName := ""
	for _, a := range d.Attributes {
		switch attr := a.(type) {
		case *tg.DocumentAttributeVideo:
			videoAttr = attr
		case *tg.DocumentAttributeAudio:
			audioAttr = attr
		case *tg.DocumentAttributeSticker:
			stickerAttr = attr
		case *tg.DocumentAttributeFilename:
			fileName = attr.FileName
		}
	}
	if stickerAttr != nil {
		return &Media{Kind: KindUnsupported, DCID: d.DCID} // 贴纸暂不支持
	}

	media := &Media{Kind: KindDocument, DCID: d.DCID, FileName: fileName, Size: d.Size}
	switch {
	case audioAttr != nil && audioAttr.Voice:
		media.Kind = KindVoice
		media.Audio = &AudioMeta{Duration: int(audioAttr.Duration)}
		media.FileName = defaultName(fileName, "voice.ogg")
	case audioAttr != nil:
		media.Kind = KindAudio
		media.Audio = &AudioMeta{
			Title:     audioAttr.Title,
			Performer: audioAttr.Performer,
			Duration:  int(audioAttr.Duration),
		}
		media.FileName = defaultName(fileName, "audio.mp3")
	case videoAttr != nil || strings.HasPrefix(d.MimeType, "video/"):
		media.Kind = KindVideo
		if videoAttr != nil {
			media.Video = &VideoMeta{
				Width:    videoAttr.W,
				Height:   videoAttr.H,
				Duration: int(videoAttr.Duration),
			}
		}
		media.Thumb = documentThumb(d)
		media.FileName = defaultName(fileName, "video.mp4")
	default:
		media.FileName = defaultName(fileName, "file.bin")
	}
	media.Location = documentLocation(d)
	return media
}

func documentLocation(d *tg.Document) tg.InputFileLocationClass {
	return &tg.InputDocumentFileLocation{
		ID:            d.ID,
		AccessHash:    d.AccessHash,
		FileReference: slices.Clone(d.FileReference),
	}
}

// documentThumb 从文档自带缩略图中选取最大的 JPEG 档，构造带 ThumbSize 的
// 下载坐标；文档无可用缩略图时返回 nil。PhotoStrippedSize 是需专用解码的
// 内联微缩图、PhotoPathSize 是 Lottie 路径预览，均不作为封面候选——与
// extractPhoto 一样只认真实字节档（PhotoSize / PhotoSizeProgressive）。
func documentThumb(d *tg.Document) *ThumbMeta {
	var best int64
	var thumbType string
	for _, th := range d.Thumbs {
		switch ps := th.(type) {
		case *tg.PhotoSize:
			if int64(ps.Size) > best {
				best, thumbType = int64(ps.Size), ps.Type
			}
		case *tg.PhotoSizeProgressive:
			for _, n := range ps.Sizes {
				if int64(n) > best {
					best, thumbType = int64(n), ps.Type
				}
			}
		}
	}
	if thumbType == "" || best <= 0 {
		return nil
	}
	return &ThumbMeta{
		Location: &tg.InputDocumentFileLocation{
			ID:            d.ID,
			AccessHash:    d.AccessHash,
			FileReference: slices.Clone(d.FileReference),
			ThumbSize:     thumbType,
		},
		Size: best,
	}
}

func defaultName(name, fallback string) string {
	if strings.TrimSpace(name) == "" {
		return fallback
	}
	return name
}
