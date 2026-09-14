/**
 * 受控重启共享动作：统一复用服务端重启 API、确认文案、加载态与受控结果反馈。
 * 触发入口可以位于设置页之外，但业务规则只在这里维护。
 */
import { restartServer } from "../../api/mutations";
import { useAdminAction, useConfirmAction } from "../shared/actions";

export const CONFIRM_RESTART_TEXT = "确定让应用优雅退出并由部署监管机制重新启动？";

export function useRestartAction() {
  const restart = useAdminAction({
    action: () => restartServer(),
    invalidate: [["settings"]],
    successText: (result) => result.message,
  });
  const confirmAction = useConfirmAction();

  const trigger = () => {
    confirmAction(CONFIRM_RESTART_TEXT, () => void restart.run(undefined));
  };

  return { pending: restart.pending, trigger };
}
