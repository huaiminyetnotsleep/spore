# 申请与配置 GitHub OAuth 登录

管理端（Web）的可选登录通道：在 GitHub 申请一个 OAuth App，把凭据填进 Spore
并绑定唯一的管理员 GitHub 账号。整个流程只需 `read:user` 权限——只读取你的
GitHub 用户名与数字 ID，**不涉及任何仓库、组织或账号设置权限**。

GitHub OAuth **不是默认登录凭据**：访问密钥永远是主登录通道，未配置 OAuth 时
登录页只显示密钥登录，OAuth 出现任何问题都不影响密钥登录。

## 第一步：在 GitHub 申请 OAuth App

1. **打开 <https://github.com/settings/developers>**（登录 GitHub 后依次
   Settings → Developer settings → OAuth Apps），点 **New OAuth App**；
   注意选 **OAuth Apps**，不是 GitHub Apps——两者凭据不通用；
2. **填写应用表单**并点 Register application：
   - Application name：随意，如 `spore`
   - Homepage URL：`https://<你的管理端域名>`，如 `https://admin.example.com`
   - Authorization callback URL：**必须一字不差**填
     `https://<你的管理端域名>/auth/github/callback`
     （如 `https://admin.example.com/auth/github/callback`），末尾不要加 `/`；
   - Application description 可留空
3. **记录 Client ID**：注册完成后页面直接显示；
4. **生成 Client Secret**：点 **Generate a new client secret**，密钥**只显示这一次**，
   立即存入密码管理器。泄露或遗忘只能重新生成（旧 Secret 随即失效，需回
   Spore 同步更新）。

回调地址的协议、域名、端口必须与最终访问管理端的地址完全一致，否则 GitHub
会在授权时报 redirect_uri 错误。域名变化时在 OAuth App 设置页同步修改即可。

## 第二步：配置到 Spore

两条路径二选一，**推荐方式 A**（Secret 加密存库，改配置不用登服务器）。
数据库配置优先于环境变量：保存过 Web 配置后环境变量不再生效。「清除凭据」
会停用通道并抹掉已保存的凭据，此后环境变量**不会**自动回落，重新启用需在
Web 重新保存凭据（需要主密钥）。

### 方式 A（推荐）：Web 管理端配置

1. **在服务器 `.env` 中配置主密钥**，用于加密存库的 Secret（未配置则 Web 无法
   保存凭据）。生成 32 字节 hex 密钥填入 `WEB_OAUTH_ENCRYPTION_KEY=` 并重启服务：

   ```bash
   openssl rand -hex 32
   ```

   主密钥只放服务器 `.env`，**不进数据库、不进备份**；
2. **用访问密钥登录管理端**，进入 **设置 → GitHub 登录**；
3. **填入 Client ID 与 Client Secret**，勾选「启用 GitHub 登录」，点保存配置。

   Secret 以 AES-256-GCM 加密后存入业务数据库，页面、日志、审计、备份中都不
   存在明文，保存后不再回显；之后再次保存时 Secret 留空表示**保留原值**。

### 方式 B（兼容旧部署）：环境变量

在服务器 `.env` 中配置并重启服务：

```bash
GITHUB_CLIENT_ID=Ov23lixxxxxxxxxxxxxx
GITHUB_CLIENT_SECRET=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

- 两个变量**必须成对出现**，只配一个服务会拒绝启动；
- 此方式不需要主密钥，但 Web 端无法持久化 Secret（「设置 → GitHub 登录」
  页面不能保存新凭据）。

## 第三步：绑定管理员 GitHub 账号

OAuth 配置完成后**仍不能直接登录**——必须先绑定唯一的 GitHub 账号：

1. 用访问密钥登录管理端，进入 **设置 → GitHub 登录**，点 **绑定 GitHub 账号**，
   跳转 GitHub 完成授权；
2. 绑定成功时服务端会先**失效全部 Web 会话**（含当前会话），因此浏览器最终会
   回到登录页，而不是停留在原页面；重新登录后在「设置 → GitHub 登录」即可看到
   已绑定状态；
3. 绑定之后只有这个 GitHub 账号（按数字 ID 匹配）能通过 OAuth 登录；其他
   GitHub 账号即使通过授权也会被拒绝，并计入登录限流；
4. 解绑同样会使全部会话失效；重新绑定会覆盖旧绑定。

绑定、解绑端点的请求/响应契约见 [API 参考 §10](../reference/api.md#_10-github-oauth-配置)。

## 验证

1. 登出管理端，登录页应出现 **「使用 GitHub 登录」** 入口（未启用时不显示）；
2. 点入口 → 跳 GitHub 授权 → 确认后回跳，应落在管理端首页 `/admin`；
3. 授权链路任何一步失败都只得到统一的通用失败提示（不带具体原因，避免泄露信息）：
   已登录的绑定流程回跳「设置 → GitHub 登录」并显示 `?oauth=error` 横幅；未登录的
   登录流程因没有会话，最终停在登录页，不会显示错误详情。

## 安全注意事项

- 回调必须走 HTTPS：OAuth App 回调地址填 `https://`，公网 TLS 由
  Caddy/Nginx 反代终结；
- Client Secret 只存在于两个地方：服务器 `.env`（方式 B）或数据库密文
  （方式 A）。**不要**放进 Compose 文件、截图、日志、工单或聊天记录；
- `WEB_OAUTH_ENCRYPTION_KEY` 只放服务器 `.env`，不进数据库备份。**主密钥丢失后
  已保存的 Secret 无法解密，GitHub 登录通道会自动停用**（即使环境变量里还有旧
  凭据也不回落），需配好主密钥后到 Web 中重新保存；
- 反向代理部署时设置 `WEB_TRUSTED_PROXY=true`，否则回调地址可能被推导成
  `http://` 导致与 OAuth App 登记的不一致（同时影响审计来源 IP）；
- OAuth 授权码单次有效，access token 只在回调内即时使用，不落库不入日志。

## 常见问题排查

### 登录页看不到「使用 GitHub 登录」

入口只在通道**已配置且启用**时显示，按顺序检查：

1. 第二步是否完成：环境变量成对配置并重启，或 Web 中已保存并勾选启用；
2. 「设置 → GitHub 登录」页顶部状态是否为「已启用」，未启用时点启用；
3. 数据库配置解密失败（主密钥丢失或不匹配）时通道静默停用，见下方
   「恢复备份 / 换机后 GitHub 登录失效」。

### GitHub 授权页报 redirect_uri 错误

`The redirect_uri is not associated with this application` 表示 Spore 本次
授权请求实际携带的回调地址，与 OAuth App 里登记的不一致（GitHub 严格逐字符
比对）。先定位差异，再对症修复：

1. **解码实际发送的地址**：报错页面的浏览器地址栏停留在
   `github.com/login/oauth/authorize?...&redirect_uri=XXX`，把 `redirect_uri`
   参数 URL 解码出来（注意 `%3A%2F%2F` 即 `://`、`%2F` 即 `/`），与 OAuth App
   登记的值逐段比对，差异只会出在协议、域名、路径三者之一；
2. **路径多了 `/admin` 前缀（常见）**：管理端页面都在 `/admin` 下，容易顺手把
   回调也登记成 `/admin/auth/github/callback`，但 OAuth 端点刻意挂在顶层，
   登记值必须是不带前缀的 `https://<域名>/auth/github/callback`。在 GitHub
   OAuth App 设置页改掉即可，保存立即生效；
3. **协议是 `http` 而非 `https`（反向代理部署最常见）**：`WEB_TRUSTED_PROXY`
   默认 `false`，Spore 不信任 `X-Forwarded-*` 头，而应用本身只监听
   `127.0.0.1:8080` 的纯 HTTP，于是回调被推导成 `http://...`。修复：

   1. 服务器 `.env` 设置 `WEB_TRUSTED_PROXY=true`；
   2. **用 `docker compose up -d` 重建容器**——`docker compose restart` 不会
      重新读取 `.env` 的修改；
   3. 确认反代转发了协议头：Caddy 的 `reverse_proxy` 默认自动带上；
      Nginx 需要显式配置：

      ```nginx
      proxy_set_header Host $host;
      proxy_set_header X-Forwarded-Proto $scheme;
      ```

   4. 重新发起登录，地址栏里 `redirect_uri` 应变为 `https%3A%2F%2F...`；
4. **域名不一致**：反代没转发 `Host` 时，`redirect_uri` 的域名会变成
   `127.0.0.1:8080`——按第 3 步的 Nginx 配置补上 `proxy_set_header Host
   $host;`（Caddy 默认已带）。

### GitHub 授权走完却没有登录成功

失败提示是统一的（防信息泄露）：绑定流程在「设置 → GitHub 登录」页看到
`?oauth=error` 横幅；登录流程则直接回到登录页。按命中概率排查：

1. **该 GitHub 账号未绑定**：只有绑定的管理员账号能登录，确认浏览器里
   登录的 GitHub 就是绑定时的账号；
2. **state 失效**：授权页停留超过 10 分钟、回退/刷新重放回调、或服务在发起
   与回调之间重启过（state 只存内存），从登录页重新发起即可；
3. **在 GitHub 授权页点了取消**：重新发起；
4. **服务端与 GitHub 网络不通**（token 交换 10 秒超时）：在服务器上
   `curl -I https://github.com` 确认出网。

### Web 保存 Secret 报错

提示需要主密钥时，说明服务器 `.env` 未配置 `WEB_OAUTH_ENCRYPTION_KEY` 或值
不是 32 字节（hex 应为 64 个字符）。配置后**重启服务**再保存。

### 清除凭据后无法重新启用

「清除凭据」会在数据库留下一份空的配置记录，此后环境变量**不再回落**，通道
保持停用；而重新启用前必须先保存凭据，保存又需要主密钥。恢复方法：在 `.env`
配好 `WEB_OAUTH_ENCRYPTION_KEY` 并重启服务，到 Web 中重新保存 Client ID 与
Secret（绑定关系不受影响，无需重新绑定）。

### 恢复备份 / 换机后 GitHub 登录失效

数据库备份含加密的 Secret 但**不含主密钥**。在新机器 `.env` 配置同一把
`WEB_OAUTH_ENCRYPTION_KEY` 并重启即可恢复；主密钥已丢失时，到 Web 中重新
保存一遍 Client ID 与 Secret（用新主密钥加密）再启用——绑定关系不受影响，
无需重新绑定。
