package web

// R2 定时备份配置 API：
//   - GET /api/v1/backup/r2：定时备份整体状态——间隔/份数（syscfg 单一
//     来源的读取口径，编辑入口已从设置页挪到备份页）+ R2 连接配置脱敏
//     回显 + 最近上传结果；
//   - POST /api/v1/backup/r2：合并保存间隔/份数（复用 syscfg 校验与审计
//     动作名，与设置页历史审计连续）与 R2 连接配置（data/r2-backup.json
//     0600 原子写）；密钥字段传掩码或空串 = 沿用已保存值；
//   - POST /api/v1/backup/r2/test：用已保存配置做只读连通性测试
//     （ListObjects，不写入任何对象）。
// 约束与 cloud-drive 配置同款：Secret 只落配置文件，不进数据库、日志、
// 审计与错误信息；GET 只回固定掩码。

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/errlog"
	"github.com/huaiminyetnotsleep/spore/internal/r2backup"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// r2SecretMask 是已保存密钥在 GET 视图中的固定占位；POST 收到掩码或
// 空串均沿用服务端已有值（与云盘配置 options 同款语义）。
const r2SecretMask = "********"

// r2TestTimeout 是连通性测试的时间窗（只读 ListObjects，正常秒级）。
const r2TestTimeout = 20 * time.Second

// apiBackupR2View 是 R2 连接配置的 GET 视图（脱敏）。
type apiBackupR2View struct {
	Enabled         bool   `json:"enabled"`
	Complete        bool   `json:"complete"` // 四要素齐备（开启的前提）
	AccountID       string `json:"account_id"`
	Bucket          string `json:"bucket"`
	Endpoint        string `json:"endpoint"`
	AccessKeyMasked string `json:"access_key_id"`     // 已配置返回掩码，否则空串
	SecretMasked    string `json:"secret_access_key"` // 同上
	LastUploadAt    int64  `json:"last_upload_at"`    // Unix 毫秒；0 = 从未上传
	LastUploadError string `json:"last_upload_error"` // 受控中文场景
}

// apiBackupScheduleView 是 GET /api/v1/backup/r2 的响应 DTO。
type apiBackupScheduleView struct {
	IntervalHours int             `json:"interval_hours"` // 0 = 关闭
	KeepCount     int             `json:"keep_count"`
	LastBackupAt  int64           `json:"last_backup_at"` // 本地快照口径（上传失败不影响）
	R2            apiBackupR2View `json:"r2"`
}

func r2View(cfg r2backup.Config) apiBackupR2View {
	view := apiBackupR2View{
		Enabled:         cfg.Enabled,
		Complete:        cfg.Complete(),
		AccountID:       cfg.AccountID,
		Bucket:          cfg.Bucket,
		LastUploadAt:    cfg.LastUploadAt,
		LastUploadError: cfg.LastUploadError,
	}
	if view.AccountID != "" {
		view.Endpoint = r2backup.Endpoint(cfg.AccountID)
	}
	if cfg.AccessKeyID != "" {
		view.AccessKeyMasked = r2SecretMask
	}
	if cfg.SecretAccessKey != "" {
		view.SecretMasked = r2SecretMask
	}
	return view
}

// buildBackupScheduleView 组装定时备份整体状态（备份页卡片唯一读取口径）。
func (s *Server) buildBackupScheduleView(ctx context.Context) (apiBackupScheduleView, error) {
	var view apiBackupScheduleView
	cfg, err := r2backup.Load(s.cfg.DataDir)
	if err != nil {
		return view, err
	}
	view.IntervalHours = syscfg.LoadBackupIntervalHours(ctx, s.st)
	view.KeepCount = syscfg.LoadBackupKeepCount(ctx, s.st)
	view.LastBackupAt = s.lastBackupAt(ctx)
	view.R2 = r2View(cfg)
	return view, nil
}

// handleAPIBackupR2Get 返回定时备份与 R2 上传配置状态。
func (s *Server) handleAPIBackupR2Get(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.backup.r2.get"
	view, err := s.buildBackupScheduleView(r.Context())
	if err != nil {
		s.log.Error("读取 R2 备份配置失败", "op", op, "error", err.Error())
		writeAPIError(w, http.StatusServiceUnavailable, string(apperr.CodeStoreUnavailable),
			apiUserMessage(string(apperr.CodeStoreUnavailable)))
		return
	}
	writeAPISingle(w, view)
}

// handleAPIBackupR2Post 合并保存定时备份配置：间隔/份数走 syscfg（与
// 设置 API 同键同审计），R2 连接四项合并入 data/r2-backup.json。任何
// 连接字段变更都会清掉上次的上传错误（旧错误对新配置无意义）。
func (s *Server) handleAPIBackupR2Post(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.backup.r2.save"
	ctx := r.Context()
	var in struct {
		IntervalHours *int `json:"interval_hours"`
		KeepCount     *int `json:"keep_count"`
		R2            *struct {
			Enabled         bool   `json:"enabled"`
			AccountID       string `json:"account_id"`
			AccessKeyID     string `json:"access_key_id"`
			SecretAccessKey string `json:"secret_access_key"`
			Bucket          string `json:"bucket"`
		} `json:"r2"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}

	// 间隔/份数：校验、比对与审计与 applySettingsUpdate 的同名块一致
	//（审计动作名沿用 settings.backup_interval / settings.backup_keep，
	// 保证设置页时代的历史审计连续可查）。
	if in.IntervalHours != nil {
		n := *in.IntervalHours
		if err := syscfg.ValidateBackupIntervalHours(n); err != nil {
			s.apiBadRequest(w, r, op, err.Error())
			return
		}
		if current := syscfg.LoadBackupIntervalHours(ctx, s.st); n != current {
			if err := syscfg.SetBackupIntervalHours(ctx, s.st, n); err != nil {
				s.writeAPIAppErr(w, r, op, apperr.Wrap(apperr.CodeInternal, err))
				return
			}
			s.audit(ctx, "settings.backup_interval", "settings", map[string]any{
				"before": current, "after": n, "effect": "即时生效"})
		}
	}
	if in.KeepCount != nil {
		n := *in.KeepCount
		if err := syscfg.ValidateBackupKeepCount(n); err != nil {
			s.apiBadRequest(w, r, op, err.Error())
			return
		}
		if current := syscfg.LoadBackupKeepCount(ctx, s.st); n != current {
			if err := syscfg.SetBackupKeepCount(ctx, s.st, n); err != nil {
				s.writeAPIAppErr(w, r, op, apperr.Wrap(apperr.CodeInternal, err))
				return
			}
			s.audit(ctx, "settings.backup_keep", "settings", map[string]any{
				"before": current, "after": n, "effect": "即时生效"})
		}
	}

	if in.R2 != nil {
		cur, err := r2backup.Load(s.cfg.DataDir)
		if err != nil {
			s.log.Error("读取 R2 备份配置失败", "op", op, "error", err.Error())
			s.writeAPIAppErr(w, r, op, apperr.Wrap(apperr.CodeStoreUnavailable, err))
			return
		}
		next := cur
		next.Enabled = in.R2.Enabled
		credentialChanged := false
		if v := strings.TrimSpace(in.R2.AccountID); v != "" && v != cur.AccountID {
			next.AccountID = v
			credentialChanged = true
		}
		if v := strings.TrimSpace(in.R2.AccessKeyID); v != "" && v != r2SecretMask && v != cur.AccessKeyID {
			next.AccessKeyID = v
			credentialChanged = true
		}
		if v := strings.TrimSpace(in.R2.SecretAccessKey); v != "" && v != r2SecretMask && v != cur.SecretAccessKey {
			next.SecretAccessKey = v
			credentialChanged = true
		}
		if v := strings.TrimSpace(in.R2.Bucket); v != "" && v != cur.Bucket {
			next.Bucket = v
			credentialChanged = true
		}
		if next.Enabled != cur.Enabled {
			credentialChanged = true
		}
		if err := next.Validate(); err != nil {
			s.apiBadRequest(w, r, op, err.Error())
			return
		}
		if credentialChanged {
			// 连接配置已变：上次的上传错误对新配置无意义，随保存清除
			next.LastUploadError = ""
		}
		if err := r2backup.Save(s.cfg.DataDir, next); err != nil {
			s.log.Error("保存 R2 备份配置失败", "op", op, "error", err.Error())
			s.writeAPIAppErr(w, r, op, apperr.Wrap(apperr.CodeInternal, err))
			return
		}
		s.audit(ctx, "backup.r2_config", "r2", map[string]any{
			"enabled":    next.Enabled,
			"account_id": next.AccountID,
			"bucket":     next.Bucket,
			"changed":    credentialChanged,
			"effect":     "即时生效",
		})
	}

	view, err := s.buildBackupScheduleView(ctx)
	if err != nil {
		s.log.Error("回读 R2 备份配置失败", "op", op, "error", err.Error())
		s.writeAPIAppErr(w, r, op, apperr.Wrap(apperr.CodeStoreUnavailable, err))
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Schedule apiBackupScheduleView `json:"backup_schedule"`
	}{apiWriteOK{OK: true}, view})
}

// handleAPIBackupR2Test 用已保存配置做一次只读连通性测试。结果本身是
// 成功的 API 调用（connected=false 不是 5xx），受控场景文案直接回显。
func (s *Server) handleAPIBackupR2Test(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.backup.r2.test"
	cfg, err := r2backup.Load(s.cfg.DataDir)
	if err != nil {
		s.log.Error("读取 R2 备份配置失败", "op", op, "error", err.Error())
		s.writeAPIAppErr(w, r, op, apperr.Wrap(apperr.CodeStoreUnavailable, err))
		return
	}
	if !cfg.Complete() {
		s.apiBadRequest(w, r, op, "请先完整保存 R2 配置（Account ID、Access Key ID、Secret 与 Bucket），再测试连接。")
		return
	}
	tctx, cancel := context.WithTimeout(r.Context(), r2TestTimeout)
	defer cancel()
	testErr := r2backup.TestConnection(tctx, cfg)
	if testErr != nil {
		scene := r2backup.ClassifyError(testErr)
		s.log.Warn("R2 连通性测试失败", "op", op, "scene", scene, "error", testErr.Error())
		// 错误日志中心：测试失败落库（分类场景 + 原始错误串，管理员事后可查）
		s.errLog.Record(r.Context(), errlog.Record{
			Source:  store.ErrorSourceBackup,
			Stage:   "test",
			Detail:  testErr.Error(),
			Message: "R2 备份连通性测试失败：" + scene,
		})
	} else {
		s.log.Info("R2 连通性测试成功", "op", op)
	}
	s.audit(r.Context(), "backup.r2_test", "r2", map[string]any{
		"connected": testErr == nil})
	writeAPISingle(w, struct {
		apiWriteOK
		Connected bool   `json:"connected"`
		Message   string `json:"message"`
	}{
		apiWriteOK: apiWriteOK{OK: true},
		Connected:  testErr == nil,
		Message: func() string {
			if testErr != nil {
				return r2backup.ClassifyError(testErr)
			}
			return "连接成功：R2 存储桶可访问。"
		}(),
	})
}
