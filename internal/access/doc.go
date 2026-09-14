// Package access 提供数据库驱动的访问控制应用服务：
// 链接提交的六步校验链（用户状态 → 重复 → 频率 → 额度 → 并发 → 队列满）、
// 额度扣减与请求落库、待审批申请（/start）、owner 豁免，
// 以及供 Web 管理端调用的审批/限额/用量重置等管理操作。
//
// 边界约定：
//   - 检查与扣减在同一个数据库事务内完成（store.Tx），杜绝并发窗口超扣；
//   - 拒绝只更新 users.last_denied_*，不建 requests 行、不扣额度；
//   - 任务执行仍由内存队列（internal/queue）负责，requests 行是状态事实来源；
//   - 通过 access 的操作不触网：审批结果通知经注入的 delivery.Sender 发送，
//     失败仅记日志，不回滚业务状态。
package access
