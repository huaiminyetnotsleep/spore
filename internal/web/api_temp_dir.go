package web

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// apiTempFile is one regular file below TEMP_DIR. Paths are always relative.
type apiTempFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Modified  int64  `json:"modified_at"`
}

type apiTempDirView struct {
	Files     []apiTempFile `json:"files"`
	FileCount int           `json:"file_count"`
	Total     int64         `json:"total_bytes"`
}

func (s *Server) handleAPITempDirGet(w http.ResponseWriter, _ *http.Request, _ session) {
	w.Header().Set("Cache-Control", "no-store")
	view, err := scanTempDir(s.cfg.TempDir)
	if err != nil {
		s.log.Error("读取临时目录失败", "op", "api.temp-dir.list", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "TEMP_DIR_UNAVAILABLE", "读取临时目录失败，请检查服务端目录权限。")
		return
	}
	writeAPISingle(w, view)
}

func (s *Server) handleAPITempDirClear(w http.ResponseWriter, _ *http.Request, _ session) {
	w.Header().Set("Cache-Control", "no-store")
	const op = "api.temp-dir.clear"
	release, ok := s.acquireTempDirMaintenance()
	if !ok {
		writeAPIError(w, http.StatusConflict, apiCodeConflict, "队列中有待处理或正在执行的任务，请等待任务结束后重试。")
		return
	}
	defer release()
	if err := clearTempDir(s.cfg.TempDir); err != nil {
		s.log.Error("清理临时目录失败", "op", op, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "TEMP_DIR_CLEAR_FAILED", "清理临时目录失败，请检查服务端目录权限。")
		return
	}
	view, err := scanTempDir(s.cfg.TempDir)
	if err != nil {
		s.log.Error("清理后读取临时目录失败", "op", op, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "TEMP_DIR_UNAVAILABLE", "临时目录已清理，但读取最新状态失败。")
		return
	}
	writeAPISingle(w, struct {
		apiWriteOK
		TempDir apiTempDirView `json:"temp_dir"`
	}{apiWriteOK{OK: true}, view})
}

func (s *Server) handleAPITempDirDelete(w http.ResponseWriter, r *http.Request, _ session) {
	w.Header().Set("Cache-Control", "no-store")
	const op = "api.temp-dir.delete"
	var in struct {
		Paths []string `json:"paths"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if len(in.Paths) == 0 || len(in.Paths) > 500 {
		s.apiBadRequest(w, r, op, "请选择 1–500 个文件后重试。")
		return
	}
	release, ok := s.acquireTempDirMaintenance()
	if !ok {
		writeAPIError(w, http.StatusConflict, apiCodeConflict, "队列中有待处理或正在执行的任务，请等待任务结束后重试。")
		return
	}
	defer release()
	if err := deleteTempFiles(s.cfg.TempDir, in.Paths); err != nil {
		if errors.Is(err, errInvalidTempPath) {
			s.apiBadRequest(w, r, op, "文件路径无效或文件已不存在，请刷新列表后重试。")
			return
		}
		s.log.Error("删除临时文件失败", "op", op, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "TEMP_DIR_DELETE_FAILED", "删除临时文件失败，请检查服务端目录权限。")
		return
	}
	view, err := scanTempDir(s.cfg.TempDir)
	if err != nil {
		s.log.Error("删除后读取临时目录失败", "op", op, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "TEMP_DIR_UNAVAILABLE", "文件已删除，但读取最新状态失败。")
		return
	}
	writeAPISingle(w, struct {
		apiWriteOK
		TempDir apiTempDirView `json:"temp_dir"`
	}{apiWriteOK{OK: true}, view})
}

func (s *Server) acquireTempDirMaintenance() (func(), bool) {
	if s.queueMaintenance == nil {
		return nil, false
	}
	return s.queueMaintenance.TryMaintenance()
}

func scanTempDir(root string) (apiTempDirView, error) {
	view := apiTempDirView{Files: []apiTempFile{}}
	rootInfo, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return view, nil
	}
	if err != nil {
		return view, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return view, errors.New("temporary directory root is not a real directory")
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		view.Files = append(view.Files, apiTempFile{
			Path:      filepath.ToSlash(rel),
			SizeBytes: info.Size(),
			Modified:  info.ModTime().UnixMilli(),
		})
		view.Total += info.Size()
		return nil
	})
	if err != nil {
		return apiTempDirView{}, err
	}
	sort.Slice(view.Files, func(i, j int) bool { return view.Files[i].Path < view.Files[j].Path })
	view.FileCount = len(view.Files)
	return view, nil
}

func clearTempDir(root string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("temporary directory root is not a real directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

var errInvalidTempPath = errors.New("invalid temporary file path")

func deleteTempFiles(root string, paths []string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return errInvalidTempPath
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("temporary directory root is not a real directory")
	}
	seen := make(map[string]struct{}, len(paths))
	targets := make([]string, 0, len(paths))
	for _, value := range paths {
		clean := filepath.Clean(filepath.FromSlash(value))
		if value == "" || filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return errInvalidTempPath
		}
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		parts := strings.Split(clean, string(filepath.Separator))
		parent := root
		for _, part := range parts[:len(parts)-1] {
			parent = filepath.Join(parent, part)
			parentInfo, err := os.Lstat(parent)
			if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
				return errInvalidTempPath
			}
		}
		target := filepath.Join(root, clean)
		targetInfo, err := os.Lstat(target)
		if err != nil || !targetInfo.Mode().IsRegular() {
			return errInvalidTempPath
		}
		targets = append(targets, target)
	}
	for _, target := range targets {
		if err := os.Remove(target); err != nil {
			return err
		}
	}
	return nil
}
