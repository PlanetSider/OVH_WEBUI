# QQ Bot 扫码注册

## Goal
在系统设置的 QQ 机器人区域加入官方 QQ Bot 扫码创建/绑定流程。用户用手机 QQ 扫描页面二维码后，服务端自动保存 AppID、AppSecret，并把扫码者 OpenID 加入管理员白名单。

## Approved Scope
- 使用官方 `q.qq.com` binding API：`/lite/create_bind_task`、`/lite/poll_bind_result`。
- 新增后端注册会话接口；浏览器只展示二维码 URL 和状态，不接触 AppSecret。
- 服务端使用 AES-256-GCM 解密 `bot_encrypt_secret`，保存凭据并重新配置 QQ client。
- 扫码完成自动追加扫码者 `user_openid`，保留已有群聊/频道目标并启用 QQ 通知。
- QQ 设置页复用现有内置二维码生成器和飞书扫码 UI 状态模式。

## Owners / Contract
- 后端 owner：`backend/internal/handlers/qq_registration.go`、`backend/main.go`、`backend/internal/types` 配置保存能力。
- 前端 owner：`src/pages/SettingsPage.tsx`、`src/hooks/ovh/use-settings.ts`。
- API：`POST /api/qq/registration/start` 返回 `sessionId`、二维码 URL、过期时间和轮询间隔；`GET /api/qq/registration/:sessionId` 返回 pending/complete/expired/error，不返回 AppSecret。
- 完成响应只返回 `appId`、`appSecretConfigured`、`userOpenId` 或绑定状态；AppSecret 只进入服务端配置密文存储。

## Security / Compatibility
- 绑定密钥使用 32 字节 `crypto/rand`，二维码 URL 固定使用 `https://q.qq.com/qqbot/openclaw/connect.html`。
- 外部请求固定限制 host、POST JSON、响应体大小和 HTTP 超时；会话内存保存，过期/完成/拒绝后删除。
- AES-GCM 解密格式按参考实现处理：Base64 解码后前 12 字节 nonce，剩余为 ciphertext+tag；错误不得输出密钥或密文。
- 不改变手动 AppID/AppSecret 配置、QQ Gateway、通知目标或历史微信数据。

## TDD Route
`auto -> strict`：新增公共 API、凭据解密、配置写入和前端轮询行为，补充后端单元测试并运行完整回归；不执行真实扫码或真实 QQ 外部副作用测试。

## Tasks
1. 增加 QQ 注册会话、外部 binding client、AES-GCM 解密、配置保存和 QQ client reconfigure；补充成功/错误/过期测试。
2. 注册路由并同步 API 合同与设置 hook 类型/API 方法。
3. 在 QQ 设置区域增加扫码按钮、二维码、轮询状态、成功回填和重试/过期 UI。
4. 运行 Go 测试、`go vet`、TypeScript、构建、diff 检查，审查凭据不泄露。
5. 提交并推送单一功能提交，回读远端和工作区状态。

## Verification
- `go test ./... -count=1`
- `go vet ./...`
- `npx tsc -b`
- `npm run build`
- `git diff --check`
- 静态审查 API 响应、日志和前端状态不包含 AppSecret。

## Execution Evidence
- `go test ./... -count=1`：通过。
- `go vet ./...`：通过。
- `npx tsc -b`：通过；`npm run build`：通过，仅有既有 npm 配置警告和 chunk 大小提示。
- `git diff --check`：通过。
- 新增测试覆盖 AES-GCM 解密、启动会话、成功轮询保存且响应不含 AppSecret、过期会话。
- 复核并修复 `qqbot.Client.Reconfigure` 与 `ensureGateway` 的锁顺序反转；QQ 相关测试和全量回归均通过。
- 未执行真实 QQ 扫码、q.qq.com 外部调用、QQ 消息或 OVH 下单/付款；部署后需使用测试 QQ 账号验证官方绑定服务和平台权限。
- 代码审查子代理因当前会话无可用 LLM provider 未启动，完成前由主会话进行静态安全复核；该限制不替代真实外部绑定验证。
