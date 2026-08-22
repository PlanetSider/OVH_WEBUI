import { AppLayout } from "@/components/layout/AppLayout";
import { Helmet } from "react-helmet-async";
import { Settings as SettingsIcon, KeyRound, Globe, Send, Database, Save, Webhook, AlertTriangle, CheckCircle2, Plus, Star, RotateCw, Trash2, Pencil, MessageSquare, QrCode, Loader2, ExternalLink } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { PageHeader } from "@/components/common/PageHeader";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/common/Skeleton";
import { Chip } from "@/components/common/Chip";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog";
import {
  useSettings,
  useSaveSettings,
  useCacheInfo,
  useClearCache,
  useTelegramWebhookInfo,
  useSetTelegramWebhook,
  useFeishuBinding,
  useSendFeishuTestCard,
  startFeishuRegistration,
  pollFeishuRegistration,
  type SettingsConfig,
} from "@/hooks/use-settings";
import { getApiSecretKey, setApiSecretKey } from "@/lib/api";
import { cn } from "@/lib/utils";
import { createQRCodeDataURL } from "@/vendor/qrcode";
import { useSearchParams } from "react-router-dom";
import { OVH_SUBSIDIARIES } from "@/lib/ovh-subsidiaries";
import {
  useAccounts,
  useCreateAccount,
  useUpdateAccount,
  useDeleteAccount,
  useSetDefaultAccount,
  useVerifyAccount,
  useAccountStatuses,
  accountChipColor,
  type OVHAccount,
} from "@/hooks/use-accounts";

/** 根据 zone 推 endpoint */
function endpointForZone(zone: string): string {
  return OVH_SUBSIDIARIES.find((s) => s.code === zone)?.endpoint || "ovh-eu";
}

/** API 设置：左 sub-nav 200px + 右 form sections */
const SECTIONS = [
  { id: "password", icon: KeyRound, label: "访问密码" },
  { id: "accounts", icon: Globe, label: "OVH 账户" },
  { id: "telegram", icon: Send, label: "Telegram" },
  { id: "feishu", icon: MessageSquare, label: "飞书" },
  { id: "cache", icon: Database, label: "缓存管理" },
] as const;

function SettingsPage() {
  const [searchParams] = useSearchParams();
  const cfg = useSettings();
  const save = useSaveSettings();
  const initialSection = searchParams.get("section");
  const [active, setActive] = useState<typeof SECTIONS[number]["id"]>(
    SECTIONS.some((section) => section.id === initialSection)
      ? initialSection as typeof SECTIONS[number]["id"]
      : "password"
  );
  const [form, setForm] = useState<SettingsConfig>({});
  const [apiKey, setApiKey] = useState("");

  useEffect(() => {
    if (cfg.data) setForm(cfg.data);
  }, [cfg.data]);

  useEffect(() => {
    setApiKey(getApiSecretKey() || "");
  }, []);

  useEffect(() => {
    const section = searchParams.get("section");
    if (SECTIONS.some((item) => item.id === section)) {
      setActive(section as typeof SECTIONS[number]["id"]);
    }
  }, [searchParams]);

  const set = <K extends keyof SettingsConfig>(k: K, v: SettingsConfig[K]) => setForm((prev) => ({ ...prev, [k]: v }));

  const onSave = async () => {
    if (apiKey) setApiSecretKey(apiKey);
    // 配置未加载完成时禁止整包覆盖，避免抹掉 tgWebhookSecret / 凭据
    if (cfg.isPending || !cfg.data) {
      toast.error("配置尚未加载完成，请稍后再保存");
      return;
    }
    // 与服务端已有配置合并；webhookUrl 不走 /settings
    const { webhookUrl: _w, ...formRest } = form;
    const base = { ...cfg.data, ...formRest };
    const zone = (base.zone || cfg.data.zone || "IE").trim();
    try {
      await save.mutateAsync({
        ...base,
        zone,
        endpoint: base.endpoint || endpointForZone(zone),
      });
    } catch {
      /* toast 已在 hook 里 */
    }
  };

  return (
    <div className="space-y-6">
      <PageHeader
        icon={SettingsIcon}
        title="API 设置"
        description="配置 OVH API 和通知设置"
        action={
          <Button onClick={onSave} disabled={save.isPending || cfg.isPending || !cfg.data}>
            <Save className="w-4 h-4" />
            {save.isPending ? "保存中..." : "保存设置"}
          </Button>
        }
      />

      <div className="grid grid-cols-1 lg:grid-cols-[200px_1fr] gap-4">
        {/* sub-nav:桌面竖向左栏,手机横向滚动 tab */}
        <nav className="lg:space-y-1 flex lg:flex-col overflow-x-auto lg:overflow-visible gap-1 lg:gap-0 -mx-3 px-3 lg:mx-0 lg:px-0">
          {SECTIONS.map((s) => {
            const Icon = s.icon;
            const a = active === s.id;
            return (
              <button
                key={s.id}
                type="button"
                onClick={() => setActive(s.id)}
                className={cn(
                  "flex items-center gap-2 px-3 py-2 rounded-md text-[13px] transition-colors whitespace-nowrap flex-shrink-0",
                  "lg:w-full lg:border-l-2",
                  a
                    ? "bg-secondary text-foreground font-medium lg:border-l-foreground"
                    : "text-muted-foreground hover:bg-muted hover:text-foreground lg:border-l-transparent"
                )}
              >
                <Icon className="w-4 h-4" />
                {s.label}
              </button>
            );
          })}
        </nav>

        {/* 右内容 */}
        <Card>
          <CardContent className="p-4 sm:p-6">
            {cfg.isPending ? (
              <Skeleton className="h-64 rounded-2xl" />
            ) : active === "password" ? (
              <Section title="访问密码 / API Secret Key">
                <Field label="访问密码 *" hint="后端 .env 中的 API_SECRET_KEY，本地仅保存在 localStorage">
                  <Input
                    type="password"
                    value={apiKey}
                    onChange={(e) => setApiKey(e.target.value)}
                    placeholder="输入访问密码"
                  />
                </Field>
              </Section>
            ) : active === "accounts" ? (
              <AccountsSection />
            ) : active === "telegram" ? (
              <TelegramSection form={form} set={set} onSaveToken={onSave} saving={save.isPending} />
            ) : active === "feishu" ? (
              <FeishuSection form={form} set={set} onSave={onSave} saving={save.isPending} />
            ) : (
              <CacheSection />
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="space-y-5">
      <h2 className="text-base font-semibold">{title}</h2>
      <div className="space-y-4">{children}</div>
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="block text-[13px] font-medium mb-1.5">{label}</label>
      {children}
      {hint && <p className="text-[11px] text-muted-foreground mt-1">{hint}</p>}
    </div>
  );
}

function FeishuSection({
  form,
  set,
  onSave,
  saving,
}: {
  form: SettingsConfig;
  set: <K extends keyof SettingsConfig>(key: K, value: SettingsConfig[K]) => void;
  onSave: () => Promise<void>;
  saving: boolean;
}) {
  const binding = useFeishuBinding();
  const bindingRefetch = useRef(binding.refetch);
  const setFieldRef = useRef(set);
  const testCard = useSendFeishuTestCard();
  const accountStatuses = useAccountStatuses(false);
  const origin = typeof window === "undefined" ? "" : window.location.origin;
  const [registration, setRegistration] = useState<{ sessionId: string; url: string; expiresAt: number; interval: number } | null>(null);
  const [registrationStatus, setRegistrationStatus] = useState<"idle" | "starting" | "pending" | "complete" | "error">("idle");
  const [registrationError, setRegistrationError] = useState("");
  const [qrCodeDataURL, setQRCodeDataURL] = useState("");

  useEffect(() => {
    bindingRefetch.current = binding.refetch;
  }, [binding.refetch]);

  useEffect(() => {
    setFieldRef.current = set;
  }, [set]);

  const startRegistration = async () => {
    setRegistrationStatus("starting");
    setRegistrationError("");
    try {
      const result = await startFeishuRegistration();
      setRegistration({
        sessionId: result.sessionId,
        url: result.verificationUriComplete,
        expiresAt: Date.now() + result.expiresIn * 1000,
        interval: Math.max(2, result.interval || 5),
      });
      setQRCodeDataURL("");
      void createQRCodeDataURL(result.verificationUriComplete)
        .then(setQRCodeDataURL)
        .catch((error) => {
          setRegistrationStatus("error");
          setRegistrationError(error instanceof Error ? error.message : "生成二维码失败");
        });
      setRegistrationStatus("pending");
    } catch (error) {
      setRegistrationStatus("error");
      setRegistrationError(error instanceof Error ? error.message : "创建扫码会话失败");
    }
  };

  useEffect(() => {
    if (!registration || registrationStatus !== "pending") return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      if (Date.now() >= registration.expiresAt) {
        if (!cancelled) { setRegistrationStatus("error"); setRegistrationError("二维码已过期，请重新生成"); }
        return;
      }
      try {
        const result = await pollFeishuRegistration(registration.sessionId);
        if (cancelled) return;
        if (result.status === "complete" && result.appId && result.appSecret) {
          setFieldRef.current("feishuAppId", result.appId);
          setFieldRef.current("feishuAppSecret", result.appSecret);
          setFieldRef.current("feishuDomain", result.domain || "feishu");
          setFieldRef.current("feishuEnabled", true);
          setRegistrationStatus("complete");
          void bindingRefetch.current();
          toast.success("飞书机器人已创建，App ID 和 App Secret 已自动回填并保存");
          return;
        }
        if (result.status !== "pending") {
          setRegistrationStatus("error");
          setRegistrationError(result.error || "扫码创建机器人失败");
          return;
        }
        timer = setTimeout(poll, Math.max(2, result.retryAfter || registration.interval) * 1000);
      } catch (error) {
        if (!cancelled) {
          setRegistrationStatus("error");
          setRegistrationError(error instanceof Error ? error.message : "查询扫码状态失败");
        }
      }
    };
    timer = setTimeout(poll, registration.interval * 1000);
    return () => { cancelled = true; if (timer) clearTimeout(timer); };
  }, [registration, registrationStatus]);

  return (
    <Section title="飞书通知与交互卡片">
      <div className="rounded-2xl border border-border p-4 space-y-4">
        <div className="flex items-center justify-between gap-3">
          <div>
            <h3 className="text-[13px] font-medium">启用飞书</h3>
            <p className="text-[11px] text-muted-foreground mt-1">只填写 App ID 和 App Secret 即可完成飞书通知配置；Verification Token、Encrypt Key 为事件回调和按钮交互的可选安全项。</p>
          </div>
          <Chip tone={form.feishuEnabled ? "success" : "warning"}>{form.feishuEnabled ? "已启用" : "待配置"}</Chip>
        </div>
        <div className="rounded-xl border border-primary/30 bg-primary/5 p-4 space-y-3">
          <div className="flex items-start justify-between gap-3">
            <div>
              <h3 className="text-[13px] font-medium flex items-center gap-2"><QrCode className="h-4 w-4" />扫码创建飞书机器人</h3>
              <p className="text-[11px] text-muted-foreground mt-1">使用飞书官方 PersonalAgent 注册流程。扫码确认后，系统自动保存并回填 App ID / App Secret，同时把扫码者绑定为全局通知接收人。</p>
            </div>
            <Button type="button" size="sm" variant="outline" onClick={() => void startRegistration()} disabled={registrationStatus === "starting" || registrationStatus === "pending"}>
              {registrationStatus === "starting" ? <Loader2 className="h-4 w-4 animate-spin" /> : <QrCode className="h-4 w-4" />}
              {registrationStatus === "pending" ? "等待扫码" : "生成二维码"}
            </Button>
          </div>
          {registration && registrationStatus === "pending" && (
            <div className="rounded-lg bg-background border p-4 text-center space-y-3">
              {qrCodeDataURL ? (
                <img src={qrCodeDataURL} alt="飞书机器人创建二维码" className="h-64 w-64 max-w-full mx-auto rounded-lg bg-white p-2" />
              ) : (
                <div className="h-64 w-64 max-w-full mx-auto rounded-lg bg-white flex items-center justify-center"><Loader2 className="h-8 w-8 animate-spin text-primary" /></div>
              )}
              <p className="text-xs text-muted-foreground">请使用飞书扫描上方二维码并确认创建机器人。二维码完全在浏览器本地生成。</p>
              <Button asChild type="button" variant="terminal" size="sm">
                <a href={registration.url} target="_blank" rel="noreferrer">打开飞书官方验证页 <ExternalLink className="h-3.5 w-3.5" /></a>
              </Button>
              <div className="flex items-center justify-center gap-2 text-xs text-primary"><Loader2 className="h-3.5 w-3.5 animate-spin" />正在等待飞书返回机器人凭据…</div>
            </div>
          )}
          {registrationStatus === "complete" && <div className="text-xs text-primary flex items-center gap-2"><CheckCircle2 className="h-4 w-4" />创建成功，凭据已自动保存并回填。</div>}
          {registrationStatus === "error" && <div className="text-xs text-destructive">{registrationError}</div>}
        </div>
        <Field label="App ID"><Input value={form.feishuAppId || ""} onChange={(e) => set("feishuAppId", e.target.value)} placeholder="cli_xxx" /></Field>
        <Field label="App Secret"><Input type="password" value={form.feishuAppSecret || ""} onChange={(e) => set("feishuAppSecret", e.target.value)} /></Field>
        <Field label="开放平台域名" hint="扫码创建时会自动识别；手动填写海外 Lark 凭据时请选择 Lark。">
          <Select value={form.feishuDomain || "feishu"} onValueChange={(value: "feishu" | "lark") => set("feishuDomain", value)}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="feishu">飞书（open.feishu.cn）</SelectItem>
              <SelectItem value="lark">Lark（open.larksuite.com）</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        <Field label="Verification Token（可选）"><Input type="password" value={form.feishuVerificationToken || ""} onChange={(e) => set("feishuVerificationToken", e.target.value)} /></Field>
        <Field label="Encrypt Key（可选）" hint="仅在飞书事件订阅启用了加密时填写；发送通知不需要此项。"><Input type="password" value={form.feishuEncryptKey || ""} onChange={(e) => set("feishuEncryptKey", e.target.value)} /></Field>
        <div className="text-[11px] text-muted-foreground">事件订阅 URL：<code className="font-mono">{origin}/api/feishu/events</code></div>
        <div className="text-[11px] text-muted-foreground">卡片回调 URL：<code className="font-mono">{origin}/api/feishu/card-action</code></div>
        <div className="flex flex-wrap gap-2">
          <Button type="button" onClick={() => void onSave()} disabled={saving}>{saving ? "保存中…" : "保存飞书配置"}</Button>
          <Button type="button" variant="outline" onClick={() => testCard.mutate()} disabled={testCard.isPending || !binding.data?.bound}>{testCard.isPending ? "发送中…" : "发送测试卡片"}</Button>
          <Button type="button" variant="outline" onClick={() => void accountStatuses.refetch()} disabled={accountStatuses.isFetching}>{accountStatuses.isFetching ? "查询账户状态…" : "查询账户状态"}</Button>
        </div>
        <div className="text-[12px]">
          <span className="text-muted-foreground">全局飞书接收人：</span>{binding.data?.bound ? <Chip tone="success">已绑定 {binding.data.binding?.openId}</Chip> : <Chip tone="warning">未绑定</Chip>}
        </div>
        {accountStatuses.data && accountStatuses.data.length > 0 && <div className="space-y-1">{accountStatuses.data.map((account) => <div key={account.id} className="flex justify-between text-[12px]"><span>{account.name}</span><Chip tone={account.valid ? "success" : "danger"}>{account.valid ? "正常" : account.error || "失败"}</Chip></div>)}</div>}
      </div>
      <p className="text-[11px] text-muted-foreground">飞书通知与 Telegram 使用同一份默认账户库存、价格和通知文案；每套配置单独发送，并提供冻结生成时账户的一次性下单按钮。私聊机器人任意消息即可更新全局接收人。</p>
    </Section>
  );
}

function TelegramSection({
  form,
  set,
  onSaveToken,
  saving,
}: {
  form: SettingsConfig;
  set: (k: keyof SettingsConfig, v: string) => void;
  onSaveToken: () => Promise<void>;
  saving: boolean;
}) {
  const webhook = useTelegramWebhookInfo();
  const setWebhook = useSetTelegramWebhook();

  // 默认填当前站点源（HTTPS 部署时通常就是正确公网域名）
  useEffect(() => {
    if (form.webhookUrl) return;
    try {
      const origin = window.location.origin;
      if (origin.startsWith("https://")) {
        set("webhookUrl", origin);
      }
    } catch {
      /* ignore */
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 从 Telegram 拉回已注册 URL 时回填
  useEffect(() => {
    if (webhook.data?.url && !form.webhookUrl) {
      // 展示完整 URL；设置时后端也会自动补全 /api/telegram/webhook
      set("webhookUrl", webhook.data.url.replace(/\/api\/telegram\/webhook\/?$/, "") || webhook.data.url);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [webhook.data?.url]);

  const onFetch = () => {
    if (!form.tgToken) {
      toast.error("请先填写并保存 Bot Token");
      return;
    }
    void webhook.refetch();
  };

  const onApplyWebhook = async () => {
    const url = (form.webhookUrl || "").trim();
    if (!url) {
      toast.error("请填写 Webhook 公网地址（如 https://ovh.example.com）");
      return;
    }
    if (!form.tgToken?.trim()) {
      toast.error("请先填写 Bot Token");
      return;
    }
    // 先落库 Token，再调 Telegram setWebhook（与「Telegram 下单」同一链路）
    try {
      await onSaveToken();
      await setWebhook.mutateAsync(url);
      void webhook.refetch();
    } catch {
      /* toast 已处理 */
    }
  };

  return (
    <Section title="Telegram 通知">
      <Field label="Bot Token" hint="保存设置后写入后端；Webhook 需再点下方「注册 Webhook」才会生效">
        <Input
          type="password"
          value={form.tgToken || ""}
          onChange={(e) => set("tgToken", e.target.value)}
          placeholder="123456:ABCdef..."
        />
      </Field>
      <Field label="Chat ID">
        <Input
          value={form.tgChatId || ""}
          onChange={(e) => set("tgChatId", e.target.value)}
          placeholder="-1001234567890"
        />
      </Field>

      <div className="rounded-2xl border border-border/80 bg-muted/20 p-4 space-y-3">
        <div>
          <h3 className="text-[13px] font-semibold flex items-center gap-1.5">
            <Webhook className="w-4 h-4 text-primary" />
            Telegram Webhook（公网 HTTPS）
          </h3>
          <p className="text-[11px] text-muted-foreground mt-1 leading-relaxed">
            Webhook 注册在 Telegram 服务器，不会只存在本地表单。填写域名根即可，后端会自动补全{" "}
            <code className="font-mono text-[10px]">/api/telegram/webhook</code>。
          </p>
        </div>
        <Field label="公网 URL">
          <Input
            value={form.webhookUrl || ""}
            onChange={(e) => set("webhookUrl", e.target.value)}
            placeholder="https://ovh.example.com"
            className="font-mono text-[13px]"
          />
        </Field>
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            onClick={() => void onApplyWebhook()}
            disabled={setWebhook.isPending || saving}
          >
            <Webhook className={cn("w-3.5 h-3.5", setWebhook.isPending && "animate-pulse")} />
            {setWebhook.isPending ? "注册中…" : "保存 Token 并注册 Webhook"}
          </Button>
          <Button type="button" variant="outline" onClick={onFetch} disabled={webhook.isFetching}>
            {webhook.isFetching ? "查询中…" : "查看当前 Webhook"}
          </Button>
        </div>
      </div>

      <div className="pt-2">
        <div className="flex items-center justify-between mb-2">
          <h3 className="text-[13px] font-medium flex items-center gap-1.5">
            <Webhook className="w-3.5 h-3.5 text-muted-foreground" />
            当前 Webhook 状态（来自 Telegram）
          </h3>
        </div>

        {webhook.isError ? (
          <div className="border border-border rounded-2xl p-4 text-[12px] text-destructive flex items-start gap-2">
            <AlertTriangle className="w-4 h-4 flex-shrink-0 mt-0.5" />
            <span>{(webhook.error as Error)?.message || "获取 webhook 信息失败"}</span>
          </div>
        ) : webhook.data ? (
          <div className="border border-border rounded-2xl p-4 space-y-2 text-[12px]">
            <InfoRow
              label="URL"
              value={
                webhook.data.url ? (
                  <code className="font-mono break-all text-foreground">{webhook.data.url}</code>
                ) : (
                  <Chip tone="warning">未设置</Chip>
                )
              }
            />
            <InfoRow
              label="待处理更新"
              value={
                <span className="font-mono">
                  {webhook.data.pending_update_count ?? 0}
                </span>
              }
            />
            {webhook.data.ip_address && (
              <InfoRow
                label="IP 地址"
                value={<code className="font-mono">{webhook.data.ip_address}</code>}
              />
            )}
            {webhook.data.max_connections != null && (
              <InfoRow
                label="最大连接数"
                value={<span className="font-mono">{webhook.data.max_connections}</span>}
              />
            )}
            {webhook.data.last_error_date ? (
              <InfoRow
                label="上次错误"
                value={
                  <div className="text-right">
                    <Chip tone="danger">
                      <AlertTriangle className="w-3 h-3" />
                      {new Date(webhook.data.last_error_date * 1000).toLocaleString("zh-CN")}
                    </Chip>
                    {webhook.data.last_error_message && (
                      <p className="mt-1 text-destructive break-words max-w-[280px]">
                        {webhook.data.last_error_message}
                      </p>
                    )}
                  </div>
                }
              />
            ) : (
              <InfoRow
                label="错误状态"
                value={
                  <Chip tone="success">
                    <CheckCircle2 className="w-3 h-3" />
                    正常
                  </Chip>
                }
              />
            )}
          </div>
        ) : (
          <p className="text-[12px] text-muted-foreground">
            点击「查看当前 Webhook」从 Telegram 拉取实时状态（非本地缓存）
          </p>
        )}
      </div>
    </Section>
  );
}

function InfoRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex justify-between items-start gap-3">
      <span className="text-muted-foreground flex-shrink-0">{label}</span>
      <span className="font-medium text-right min-w-0">{value}</span>
    </div>
  );
}

function CacheSection() {
  const info = useCacheInfo();
  const clear = useClearCache();
  const sqliteUpdated = info.data?.sqlite?.updatedAtMs
    ? new Date(info.data.sqlite.updatedAtMs).toLocaleString("zh-CN")
    : "从未刷新";
  return (
    <Section title="缓存管理">
      {info.isPending ? (
        <Skeleton className="h-32 rounded-2xl" />
      ) : (
        <div className="border border-border rounded-2xl p-4 space-y-2.5 text-[13px]">
          <Row label="内存缓存条数" value={info.data?.backend?.serverCount ?? 0} />
          <Row label="内存缓存状态" value={info.data?.backend?.cacheValid ? "有效" : "已过期"} />
          <Row label="SQLite 缓存条数" value={info.data?.sqlite?.serverCount ?? 0} />
          <Row label="SQLite 最近刷新" value={<span className="text-[12px]">{sqliteUpdated}</span>} />
          <Row
            label="数据库位置"
            value={
              <code className="text-[11px] font-mono">
                {info.data?.sqlite?.path || info.data?.storage?.dataDir || "—"}
              </code>
            }
          />
        </div>
      )}
      <p className="text-[11px] text-muted-foreground">
        缓存只指 OVH 服务器目录。订阅 / 队列 / 历史 等业务数据不在此清理范围内。
      </p>
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" onClick={() => clear.mutate("memory")} disabled={clear.isPending}>
          清除内存缓存
        </Button>
        <Button variant="outline" onClick={() => clear.mutate("sqlite")} disabled={clear.isPending}>
          清除 SQLite 缓存
        </Button>
        <Button variant="destructive" onClick={() => clear.mutate("all")} disabled={clear.isPending}>
          清除全部
        </Button>
      </div>
    </Section>
  );
}

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex justify-between items-center gap-2">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-medium text-right">{value}</span>
    </div>
  );
}

// ─── 账户管理 ───────────────────────────────────────────────────────────────

function AccountsSection() {
  const accounts = useAccounts();
  const [showAdd, setShowAdd] = useState(false);
  const [editAcc, setEditAcc] = useState<OVHAccount | null>(null);
  const list = accounts.data || [];

  return (
    <Section title="OVH 账户管理">
      <div className="flex items-center justify-between">
        <p className="text-[12px] text-muted-foreground">
          每个 OVH 账户(凭据)单独保存,抢购队列 / 狙击 / 订阅创建时各自指定账户。删账户会一并清除关联的 queue / history / sniper tasks。
        </p>
        <Button onClick={() => setShowAdd(true)} size="sm">
          <Plus className="w-4 h-4" />
          添加账户
        </Button>
      </div>

      {accounts.isPending ? (
        <div className="space-y-2">
          {Array.from({ length: 2 }).map((_, i) => (
            <Skeleton key={i} className="h-24 rounded-2xl" />
          ))}
        </div>
      ) : list.length === 0 ? (
        <Card>
          <CardContent className="p-8 text-center text-sm text-muted-foreground">
            还没有账户,点右上角"添加账户"创建一个
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-3">
          {list.map((a) => (
            <AccountCard key={a.id} acc={a} onEdit={() => setEditAcc(a)} />
          ))}
        </div>
      )}

      {showAdd && <AccountDialog onClose={() => setShowAdd(false)} />}
      {editAcc && <AccountDialog acc={editAcc} onClose={() => setEditAcc(null)} />}
    </Section>
  );
}

function AccountCard({ acc, onEdit }: { acc: OVHAccount; onEdit: () => void }) {
  const setDefault = useSetDefaultAccount();
  const del = useDeleteAccount();
  const verify = useVerifyAccount();
  const [confirming, setConfirming] = useState(false);

  return (
    <div className="border border-border rounded-2xl p-4 flex flex-col sm:flex-row sm:items-center gap-3">
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 mb-1 flex-wrap">
          <span className="font-semibold text-sm">{acc.name}</span>
          <span className={cn("inline-flex items-center px-2 py-0.5 rounded text-[11px] font-medium", accountChipColor(acc.zone))}>
            {acc.zone}
          </span>
          {acc.isDefault && (
            <Chip tone="success">
              <Star className="w-3 h-3" />
              默认
            </Chip>
          )}
        </div>
        <div className="text-[11px] text-muted-foreground flex items-center gap-2 flex-wrap font-mono">
          <span>{acc.endpoint}</span>
          <span>·</span>
          <span>{acc.iam}</span>
          <span>·</span>
          <span>建于 {new Date(acc.createdAt).toLocaleDateString("zh-CN")}</span>
        </div>
      </div>
      <div className="flex items-center gap-2 flex-shrink-0">
        <Button variant="ghost" size="icon" onClick={() => verify.mutate(acc.id)} disabled={verify.isPending} title="重新验证凭据">
          <RotateCw className={cn("w-4 h-4", verify.isPending && "animate-spin")} />
        </Button>
        {!acc.isDefault && (
          <Button variant="ghost" size="icon" onClick={() => setDefault.mutate(acc.id)} disabled={setDefault.isPending} title="设为默认">
            <Star className="w-4 h-4" />
          </Button>
        )}
        <Button variant="ghost" size="icon" onClick={onEdit} title="编辑">
          <Pencil className="w-4 h-4" />
        </Button>
        <Button variant="ghost" size="icon" onClick={() => setConfirming(true)} title="删除" className="text-destructive hover:text-destructive">
          <Trash2 className="w-4 h-4" />
        </Button>
      </div>

      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent className="w-[95vw] sm:w-full sm:max-w-md">
          <DialogHeader>
            <DialogTitle>确认删除账户 {acc.name}?</DialogTitle>
            <DialogDescription className="text-destructive">
              将级联删除该账户的所有 queue 任务、history 历史。
              监控订阅的 auto_order 引用此账户的会清空。该操作不可逆。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirming(false)}>取消</Button>
            <Button
              variant="destructive"
              onClick={async () => {
                await del.mutateAsync(acc.id);
                setConfirming(false);
              }}
              disabled={del.isPending}
            >
              确认删除
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function AccountDialog({ acc, onClose }: { acc?: OVHAccount; onClose: () => void }) {
  const create = useCreateAccount();
  const update = useUpdateAccount();
  const isEdit = !!acc;
  const [form, setForm] = useState({
    name: acc?.name || "",
    appKey: acc?.appKey || "",
    appSecret: acc?.appSecret || "",
    consumerKey: acc?.consumerKey || "",
    zone: acc?.zone || "IE",
  });
  const set = (k: keyof typeof form, v: string) => setForm((p) => ({ ...p, [k]: v }));
  const canSubmit = form.name.trim() && form.appKey.trim() && form.appSecret.trim() && form.consumerKey.trim();

  const submit = async () => {
    if (!canSubmit) return;
    const payload = {
      name: form.name.trim(),
      appKey: form.appKey.trim(),
      appSecret: form.appSecret.trim(),
      consumerKey: form.consumerKey.trim(),
      zone: form.zone,
      endpoint: endpointForZone(form.zone),
    };
    if (isEdit) {
      await update.mutateAsync({ id: acc!.id, input: payload });
    } else {
      await create.mutateAsync(payload);
    }
    onClose();
  };

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="w-[95vw] sm:w-full sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{isEdit ? `编辑账户 ${acc!.name}` : "添加 OVH 账户"}</DialogTitle>
          <DialogDescription>填三个 OVH 密钥 + 选子公司,保存时会自动调 /me 验证凭据。</DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-2">
          <Field label="账户名称 *">
            <Input value={form.name} onChange={(e) => set("name", e.target.value)} placeholder="主号 / 小号 A" autoFocus />
          </Field>
          <Field label="APP KEY *">
            <Input type="password" value={form.appKey} onChange={(e) => set("appKey", e.target.value)} placeholder="xxxxxxxxxxxxxxxx" />
          </Field>
          <Field label="APP SECRET *">
            <Input type="password" value={form.appSecret} onChange={(e) => set("appSecret", e.target.value)} placeholder="xxxxxxxxxxxxxxxx" />
          </Field>
          <Field label="CONSUMER KEY *">
            <Input type="password" value={form.consumerKey} onChange={(e) => set("consumerKey", e.target.value)} placeholder="xxxxxxxxxxxxxxxx" />
          </Field>
          <Field
            label="OVH 子公司 (Zone)"
            hint={`Endpoint ${endpointForZone(form.zone)} · IAM go-ovh-${form.zone.toLowerCase()} 由子公司自动派生`}
          >
            <Select value={form.zone} onValueChange={(v) => set("zone", v)}>
              <SelectTrigger className="h-11">
                <SelectValue placeholder="请选择子公司" />
              </SelectTrigger>
              <SelectContent className="z-[300] max-h-[min(20rem,50vh)]">
                {OVH_SUBSIDIARIES.map((s) => (
                  <SelectItem key={s.code} value={s.code}>
                    {s.code} · {s.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>取消</Button>
          <Button onClick={submit} disabled={!canSubmit || create.isPending || update.isPending}>
            {(create.isPending || update.isPending) ? "保存中…" : "保存并验证"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}


const Page = () => (
  <>
    <Helmet>
      <title>系统设置 | OVH WebUI</title>
    </Helmet>
    <AppLayout>
      <SettingsPage />
    </AppLayout>
  </>
);

export default Page;
