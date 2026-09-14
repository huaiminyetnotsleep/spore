// Package cloudarchive 实现 /download 指令的网盘下载基础能力：
// 目的地配置（data/cloud-drive.json，0600 原子写 + 内存快照热加载）、
// rclone 子进程上传桥（RcloneSink）与远端路径构建（BuildPlan）。
//
// 依赖边界：本包仅依赖 apperr 与标准库，不依赖 queue/web/botapi（被它们依赖）。
// 数据红线：options 中可能含网盘凭据（rclone obscure 值），只驻留内存与
// 0600 配置文件，不进数据库、不进日志、不进错误信息。
package cloudarchive
