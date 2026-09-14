/** 读取 Go SPA 入口注入的当前文档 CSP nonce。开发服务器没有 nonce 时返回 undefined。 */
import { PROJECT_IDENTITY } from "./shared/projectIdentity.generated";

const CSP_NONCE_PLACEHOLDER = PROJECT_IDENTITY.cspNoncePlaceholder;

export function readCSPNonce(doc: Document = document): string | undefined {
  const value = doc.querySelector<HTMLMetaElement>('meta[name="csp-nonce"]')?.content.trim();
  return value && value !== CSP_NONCE_PLACEHOLDER ? value : undefined;
}

/** 生成 Ant Design ConfigProvider 所需的最小 CSP 配置。 */
export function cspConfig(nonce: string | undefined): { nonce: string } | undefined {
  return nonce ? { nonce } : undefined;
}
