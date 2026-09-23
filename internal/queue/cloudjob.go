package queue

// cloudjob.go — /download 云盘任务：fetch → 转换 → 逐媒体下载并上传到
// 网盘目的地（不重发回 Telegram）。与 runJob 共享 markStarted/取消标记/
// 进度注册与编辑器/自适应超时（Process 层分流）；频道副本与脚注只对产生
// TG 消息的路径有意义，云盘任务天然跳过。

import (
	"context"
	"errors"
	"html"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/cloudarchive"
	"github.com/huaiminyetnotsleep/spore/internal/errlog"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// cloudVerifyTimeout 是已上传核验（远端列举）的独立时间窗：目录列举不应
// 长于分钟级，超时按"核验不可用"失败收尾（提示稍后重试），不占用任务的
// 下载上传时限。
const cloudVerifyTimeout = 2 * time.Minute

// runCloudJob 执行云盘下载任务，返回媒体元数据、确认文案所需的远端路径
// 列表（相册合并为一行目录）、是否命中"已上传跳过"与错误。流程：
//  1. 依赖与目的地解析（nil 防御：装配错误直接失败，不静默降级）；
//     1b. 同链接同目的地已上传去重：本地命中 + 远端核验通过 → 跳过后续全部
//     步骤直接成功（复用历史元数据与路径）；核验失败 → CLOUD_VERIFY_FAILED；
//  2. fetch（15 分钟取数窗口，复用）→ message.Convert；
//  3. 全部纯文本 → CLOUD_TEXT_ONLY 失败（无 cloud_uploads 行）；
//     含不支持类型 → MEDIA_UNSUPPORTED（与原路径同语义）；
//  4. BuildPlan 规划远端布局 → 逐文件 openWithRefresh + Sink.Upload，
//     每个文件先落 uploading 行再写终态；任一失败即整体失败，
//     已成功文件的记录保留；
//  5. 取消（IsRequestCancelled）与进程退出（ctx 取消）走 Process 的
//     既有收尾路径；sink 内部已终止子进程并清理远端残件。
func runCloudJob(ctx context.Context, d Deps, j Job) (mediaMeta, []string, bool, error) {
	if d.CloudSink == nil || d.CloudCfg == nil {
		return mediaMeta{}, nil, false, apperr.New(apperr.CodeCloudUploadFailed,
			"云盘上传依赖未装配（CloudSink/CloudCfg 为空）")
	}
	dest, ok := d.CloudCfg.ResolveCloudDestination(j.CloudDest)
	if !ok {
		// 目的地在排队后被删除/停用：明确失败优于静默跳过（列表与重试可见原因）
		return mediaMeta{}, nil, false, apperr.New(apperr.CodeCloudUploadFailed,
			"云盘目的地不可用: "+j.CloudDest)
	}

	if meta, paths, skipped, err := trySkipCloudUpload(ctx, d, j, dest); err != nil || skipped {
		return meta, paths, skipped, err
	}

	fetchCtx, cancelFetch := context.WithTimeout(ctx, processTimeout)
	msgs, err := d.Fetcher.Fetch(fetchCtx, j.Ref)
	cancelFetch()
	if err != nil {
		return mediaMeta{}, nil, false, err
	}
	items := message.Convert(msgs)
	if len(items) == 0 {
		return mediaMeta{}, nil, false, apperr.New(apperr.CodeServiceMessage, "源消息无可提取内容")
	}
	meta := mediaMetaOf(items)
	if !meta.Track.hasMedia {
		return meta, nil, false, apperr.New(apperr.CodeCloudTextOnly, "纯文本消息不支持网盘下载")
	}
	for _, it := range items {
		if it.Media != nil && it.Media.Kind == message.KindUnsupported {
			return meta, nil, false, apperr.New(apperr.CodeMediaUnsupported, "该消息包含暂不支持的内容类型")
		}
	}

	// 进度总量 = 全部媒体大小之和（caption.txt 不计入，展示语义与 TG 路径一致）
	d.Progress.AddTotal(j.RequestID, meta.FileSize)
	plan := cloudarchive.BuildPlan(dest.PathPrefix, cloudPlanInput(j, msgs, items))

	sendCtx, cancelSend := context.WithTimeout(ctx, jobTimeout(meta.FileSize))
	defer cancelSend()

	refs := cloudMediaRefs(items)
	caption := cloudCaption(items)
	prog := &cloudUploadProgress{d: d, j: j}
	for _, f := range plan.Files {
		if sendCtx.Err() != nil { // 取消/超时先于下个文件：立即收尾
			return meta, nil, false, mapCloudUploadError(sendCtx.Err())
		}
		if err := uploadCloudFile(sendCtx, d, j, dest, f, refs, caption, prog); err != nil {
			return meta, nil, false, mapCloudUploadError(err)
		}
	}
	return meta, cloudDisplayPaths(plan), false, nil
}

// trySkipCloudUpload 实现同链接同目的地的"已上传跳过"：
//   - 本地命中最近一次成功云盘请求（同用户+同链接+同目的地；半成功的上次
//     任务请求状态是 failed，天然不会命中）；
//   - 远端核验其成功上传路径（文件可能已被删除）；
//   - 全部存在 → skipped=true，复用历史元数据与路径；
//   - 有路径确定不存在 → skipped=false，调用方走正常重传；
//   - 核验本身失败（网络/限流/超时）→ 错误（CLOUD_VERIFY_FAILED）：既不
//     误报成功也不盲目重传，提示用户稍后重试；
//   - 历史记录缺失或查询失败 → 保守走正常上传（查询失败只记日志不阻断）。
func trySkipCloudUpload(ctx context.Context, d Deps, j Job, dest cloudarchive.Destination) (mediaMeta, []string, bool, error) {
	if d.Store == nil {
		return mediaMeta{}, nil, false, nil
	}
	prior, err := d.Store.LatestSucceededCloudRequest(ctx, j.UserID,
		refChannelKey(j.Ref), j.Ref.MessageID, j.CloudDest)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			d.Log.Warn("查询历史云盘上传失败", "job_id", j.ID, "error", err.Error())
		}
		return mediaMeta{}, nil, false, nil
	}
	paths, ok := reusedUploadPaths(ctx, d, prior.ID)
	if !ok {
		return mediaMeta{}, nil, false, nil
	}
	verifyCtx, cancel := context.WithTimeout(ctx, cloudVerifyTimeout)
	defer cancel()
	verified, verr := d.CloudSink.VerifyUploaded(verifyCtx, dest, paths)
	if verr != nil {
		if ctx.Err() != nil {
			// 任务级取消/进程退出：保留原始 ctx 错误，交由 Process 收尾路径
			return mediaMeta{}, nil, false, verr
		}
		return mediaMeta{}, nil, false, apperr.Wrap(apperr.CodeCloudVerifyFailed, verr)
	}
	if !verified {
		d.Log.Info("历史云盘文件已不在远端，执行重传",
			"job_id", j.ID, "prior_request_id", prior.ID, "cloud_dest", j.CloudDest)
		return mediaMeta{}, nil, false, nil
	}
	d.Log.Info("同链接已上传且远端核验通过，跳过重复下载上传",
		"job_id", j.ID, "request_id", j.RequestID, "prior_request_id", prior.ID,
		"ref", j.Ref.String(), "cloud_dest", j.CloudDest)
	return metaFromRequest(prior), reusedDisplayPaths(paths), true, nil
}

// refChannelKey 与 access.ChannelKey 同构（queue 不得 import access：
// access 依赖 queue 入队，反向引用成环）：公开频道 username、私有频道
// -100 前缀数字 ID。
func refChannelKey(ref tmeurl.SourceRef) string {
	if ref.Kind == tmeurl.PeerChannelID {
		return "-100" + strconv.FormatInt(ref.ChannelID, 10)
	}
	return ref.Username
}

// reusedUploadPaths 取历史请求全部成功上传行的远端路径；无成功行（记录
// 缺失或全部失败）返回 false，调用方走正常上传。
func reusedUploadPaths(ctx context.Context, d Deps, requestID int64) ([]string, bool) {
	rows, err := d.Store.CloudUploadsByRequest(ctx, requestID)
	if err != nil {
		d.Log.Warn("查询历史云盘上传记录失败", "request_id", requestID, "error", err.Error())
		return nil, false
	}
	var paths []string
	for _, u := range rows {
		if u.Status == store.CloudUploadSucceeded && u.RemotePath != "" {
			paths = append(paths, u.RemotePath)
		}
	}
	return paths, len(paths) > 0
}

// metaFromRequest 把历史成功请求的落库元数据还原为 worker 的媒体元数据
// （跳过重复下载上传时复用，供本次请求终态落库与统计使用；云盘跳过与
// TG 复用共用）。
func metaFromRequest(r store.Request) mediaMeta {
	return mediaMeta{
		MediaType:  r.MediaType,
		MediaTypes: r.MediaTypes,
		FileSize:   r.FileSize,
		FileName:   r.FileName,
		MediaDCIDs: r.SourceMediaDCIDs,
	}
}

// reusedDisplayPaths 生成复用历史上传时的确认路径：多条路径共享同一父目录
// （相册成夹布局）时折叠为一行目录，与 cloudDisplayPaths 的展示语义一致。
func reusedDisplayPaths(paths []string) []string {
	if len(paths) <= 1 {
		return paths
	}
	parent := ""
	for i, p := range paths {
		dir := ""
		if idx := strings.LastIndexByte(p, '/'); idx >= 0 {
			dir = p[:idx]
		}
		if i == 0 {
			parent = dir
		} else if dir != parent {
			return paths
		}
	}
	return []string{parent}
}

// cloudMediaRef 是布局规划媒体项在 worker 侧的对应物：
// 持有打开句柄所需的全部信息（PlanFile.MediaIndex 指向该切片）。
type cloudMediaRef struct {
	media  message.Media
	itemID int
}

// cloudMediaRefs 按布局输入顺序收集媒体条目；photo 的文件名交由布局层
// 以 photo-{msgid}-{i}.jpg 规则生成。
func cloudMediaRefs(items []message.Item) []cloudMediaRef {
	refs := make([]cloudMediaRef, 0, len(items))
	for _, it := range items {
		if it.Media == nil {
			continue
		}
		refs = append(refs, cloudMediaRef{media: *it.Media, itemID: it.ID})
	}
	return refs
}

// cloudCaption 汇总消息文字：相册按成员顺序拼接（Telegram 把相册 caption
// 分散在各成员上），单媒体即自身配文；空串表示无配文。
func cloudCaption(items []message.Item) string {
	var parts []string
	for _, it := range items {
		if strings.TrimSpace(it.Text) != "" {
			parts = append(parts, it.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// cloudPlanInput 组装布局输入：频道名（refChannelKey，与 access.ChannelKey
// 同构）、源消息日期（UTC）与媒体列表。Item 不携带 Date，从原始 tg.Message
// 透传。
func cloudPlanInput(j Job, msgs []*tg.Message, items []message.Item) cloudarchive.PlanInput {
	in := cloudarchive.PlanInput{MessageID: j.Ref.MessageID, ChannelName: refChannelKey(j.Ref)}
	if len(msgs) > 0 && msgs[0] != nil {
		in.Date = time.Unix(int64(msgs[0].Date), 0).UTC()
	}
	for _, it := range items {
		if it.Media == nil {
			continue
		}
		if it.Media.Kind == message.KindPhoto {
			in.Media = append(in.Media, cloudarchive.PlanMedia{IsPhoto: true})
			continue
		}
		in.Media = append(in.Media, cloudarchive.PlanMedia{FileName: it.Media.FileName})
	}
	in.Caption = cloudCaption(items)
	return in
}

// uploadCloudFile 上传单个规划文件：打开媒体句柄并把 Reader 交给 Sink.Upload
// （包括临时文件的 fileGate 流式读取）→ 落 uploading 行 → 写终态 → 清理句柄
// （defer，无论成败）。caption 文件直接以内存文本上传。
func uploadCloudFile(ctx context.Context, d Deps, j Job, dest cloudarchive.Destination,
	f cloudarchive.PlanFile, refs []cloudMediaRef, caption string, prog *cloudUploadProgress) error {

	spec := cloudarchive.UploadSpec{RemotePath: f.RemotePath}
	countsProgress := false // 媒体字节计入 UploadedBytes；caption.txt 不在总量内
	if f.IsCaption {
		spec.Reader = strings.NewReader(caption)
		spec.Size = int64(len(caption))
	} else {
		ref := refs[f.MediaIndex]
		h, err := openWithRefresh(ctx, d, j, ref.media, ref.itemID)
		if err != nil {
			return err
		}
		defer func() {
			if h.Cleanup != nil {
				h.Cleanup()
			}
		}()
		spec.Size = ref.media.Size
		// 所有媒体都通过 Reader 上传。大文件的 Reader 是 fileGate：
		// rclone 读取已落盘前缀时，Telegram 下载仍可继续写入后续分片。
		spec.Reader = h.Reader
		prog.begin()

		spec.OnProgress = prog.onProgress
		countsProgress = true
	}

	rowID := insertCloudUploadRow(ctx, d, j, dest, f)
	err := d.CloudSink.Upload(ctx, dest, spec)
	if err == nil && countsProgress {
		prog.finish(spec.Size) // 成功补齐剩余字节，进度条收满（stats 可能停在 99%）
	}
	if rowID != 0 {
		finishCloudUploadRow(ctx, d, j, rowID, err, spec.Size)
	}
	// 错误日志中心：单文件级失败留痕（含远端路径与目的地，比终态记录多
	// "哪个文件"维度；取消/超时不算文件级失败，与终态行同口径）
	if err != nil && !isContextErr(err) {
		ae := apperr.From(err)
		fileCtx := jobLogContext(j)
		fileCtx["destination"] = dest.Name
		fileCtx["remote_path"] = f.RemotePath
		d.ErrLog.Record(ctx, errlog.Record{
			Source:    store.ErrorSourceRequest,
			Code:      string(ae.Code),
			Stage:     errorStage(ae.Code),
			Severity:  store.ErrorSeverityError,
			Message:   "云盘文件上传失败：" + f.FileName,
			Detail:    errorDetailRaw(ae),
			Context:   fileCtx,
			RequestID: j.RequestID,
		})
	}
	return err
}

// insertCloudUploadRow 上传前落 uploading 行；写失败按 store 既有姿态
// 只记日志与事件、不阻断上传（终态仍会尝试补写）。
func insertCloudUploadRow(ctx context.Context, d Deps, j Job, dest cloudarchive.Destination, f cloudarchive.PlanFile) int64 {
	if d.Store == nil || j.RequestID == 0 {
		return 0
	}
	row, err := d.Store.InsertCloudUpload(ctx, store.CloudUpload{
		RequestID: j.RequestID, Destination: dest.Name,
		RemotePath: f.RemotePath, FileName: f.FileName,
	})
	if err != nil {
		d.Log.Warn("创建云盘上传记录失败", "job_id", j.ID, "request_id", j.RequestID, "error", err.Error())
		if d.Events != nil {
			d.Events.StoreWriteFailed(ctx, "云盘上传记录落库")
		}
		return 0
	}
	return row.ID
}

// finishCloudUploadRow 落单文件终态（失败取 apperr 错误码；取消/超时
// 不落 failed 行——上传中断不算文件级失败，uploading 行保留中断现场，
// 请求终态由取消/INTERRUPTED 路径收尾）。用剥离取消信号的短窗口：
// 上传因取消/超时失败时原 ctx 已死。
func finishCloudUploadRow(ctx context.Context, d Deps, j Job, rowID int64, uploadErr error, size int64) {
	if d.Store == nil || rowID == 0 {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownWindow)
	defer cancel()
	if uploadErr != nil {
		if isContextErr(uploadErr) {
			return
		}
		ae := apperr.From(uploadErr)
		if err := d.Store.FinishCloudUpload(wctx, rowID, store.CloudUploadFailed,
			string(ae.Code), errorDetailText(ae), 0, 0); err != nil {
			d.Log.Warn("落库云盘上传失败终态失败", "job_id", j.ID, "error", err.Error())
		}
		return
	}
	if err := d.Store.FinishCloudUpload(wctx, rowID, store.CloudUploadSucceeded, "", "", size, 0); err != nil {
		d.Log.Warn("落库云盘上传成功终态失败", "job_id", j.ID, "error", err.Error())
	}
}

// mapCloudUploadError 将云盘发送阶段的 deadline 映射为明确的业务错误；
// 取消仍保留原始 context 错误，以便 Process 走取消收尾路径。
func mapCloudUploadError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return apperr.Wrap(apperr.CodeCloudUploadTimeout, err)
	}
	return err
}

// isContextErr 判断错误链是否为纯 ctx 取消/超时（errors.Is 穿透包装）。
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// cloudUploadProgress 把 Sink 的"当前文件绝对值"进度换算为 Registry 的
// 增量累加：多文件任务的总上传量 = 各文件增量之和，与
// "偏移累加"等价实现）。RcloneSink 经单调门串行回调；加锁以兼容
// 其他并发上报的 Sink 实现。
type cloudUploadProgress struct {
	d    Deps
	j    Job
	mu   sync.Mutex
	last int64 // 当前文件最近一次上报的绝对值
}

func (p *cloudUploadProgress) begin() {
	p.mu.Lock()
	p.last = 0
	p.mu.Unlock()
}

func (p *cloudUploadProgress) onProgress(sent int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if delta := sent - p.last; delta > 0 {
		p.d.Progress.AddUploaded(p.j.RequestID, delta)
	}
	p.last = sent
}

// finish 在文件成功收尾时把进度补齐到文件完整大小（stats 可能停在 99%）。
// 失败路径不调用：进度停留在最后一次真实上报附近。
func (p *cloudUploadProgress) finish(size int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if size > p.last {
		p.d.Progress.AddUploaded(p.j.RequestID, size-p.last)
	}
	p.last = 0
}

// cloudDisplayPaths 生成确认文案的路径列表：成夹布局（相册/带配文）
// 合并为一行目录，平铺布局列出文件。
func cloudDisplayPaths(plan cloudarchive.Plan) []string {
	if plan.Grouped {
		return []string{plan.Dir}
	}
	out := make([]string, 0, len(plan.Files))
	for _, f := range plan.Files {
		out = append(out, f.RemotePath)
	}
	return out
}

// sendCloudConfirm 发送云盘成功确认文本：目的地加粗、远端路径列表以引用块
// 分组（相册合并为一行目录）、原消息以来源卡片样式附后（与其他任务终态文案
// 同款）、网盘官网行可点击（已知后端类型才有）；skipped 时附"未重复下载"
// 说明。路径与链接中的用户内容经 HTML 转义后拼接；发送失败只记日志。
func sendCloudConfirm(ctx context.Context, d Deps, j Job, paths []string, skipped bool) {
	if len(paths) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("☁️ 已上传到网盘 <b>")
	b.WriteString(html.EscapeString(j.CloudDest))
	b.WriteString("</b>：\n<blockquote>")
	for i, p := range paths {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(html.EscapeString(p))
	}
	b.WriteString("</blockquote>")
	if skipped {
		b.WriteString("\n\n该链接此前已上传，本次未重复下载。")
	}
	if link, ok := j.Ref.URL(); ok {
		b.WriteString("\n\n")
		b.WriteString(sourceLinkCardHTML(sourceLinkAnchorHTML(link)))
	}
	if site := cloudProviderSite(d, j); site != "" {
		b.WriteString("\n\n🌐 网盘官网：<a href=\"")
		b.WriteString(html.EscapeString(site))
		b.WriteString("\">")
		b.WriteString(html.EscapeString(site))
		b.WriteString("</a>")
	}
	if _, err := d.senderFor(j).SendMessage(ctx, j.ChatID, b.String()); err != nil {
		d.Log.Warn("云盘确认文本发送失败", "job_id", j.ID, "error", err.Error())
		d.ErrLog.Record(ctx, warnErrorLog(j, store.ErrorSourceBotAPI, "finalize",
			"云盘确认文本发送失败", err))
	}
}

// cloudProviderSite 解析任务目的地的后端类型并映射服务商官网（内置映射，
// 不经配置注入）；目的地已被删除/停用或类型未知时返回空串，确认文案省略
// 官网行。
func cloudProviderSite(d Deps, j Job) string {
	if d.CloudCfg == nil {
		return ""
	}
	dest, ok := d.CloudCfg.ResolveCloudDestination(j.CloudDest)
	if !ok {
		return ""
	}
	return cloudarchive.ProviderHomeSite(dest.Type)
}
