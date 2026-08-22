import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/http";
import { qk } from "@/lib/query";
import { toast } from "sonner";

export interface SettingsConfig {
  appKey?: string;
  appSecret?: string;
  consumerKey?: string;
  endpoint?: string;
  zone?: string;
  iam?: string;
  tgToken?: string;
  tgChatId?: string;
  webhookUrl?: string;
  feishuEnabled?: boolean;
  feishuAppId?: string;
  feishuAppSecret?: string;
  feishuVerificationToken?: string;
  feishuEncryptKey?: string;
}

export interface FeishuBinding {
  accountId?: string;
  openId?: string;
  name?: string;
  updatedAt?: string;
}

function apiErrorText(error: unknown, fallback: string) {
  const value = error as { response?: { data?: { error?: string; message?: string } }; message?: string };
  return value.response?.data?.error || value.response?.data?.message || value.message || fallback;
}

export function useFeishuBinding(accountId?: string, enabled = true) {
  return useQuery({
    queryKey: ["feishu", "binding", accountId || "default"],
    queryFn: async () => (await api.get<{ success: boolean; bound: boolean; binding?: FeishuBinding }>("/feishu/binding", { params: { account: accountId } })).data,
    enabled,
  });
}

export function useSendFeishuTestCard(accountId?: string) {
  return useMutation({
    mutationFn: async () => (await api.post("/feishu/test-card", undefined, { params: { account: accountId } })).data,
    onSuccess: () => toast.success("测试交互卡片已发送"),
    onError: (error: unknown) => toast.error(apiErrorText(error, "发送飞书测试卡片失败")),
  });
}

export interface TelegramWebhookInfo {
  url?: string;
  has_custom_certificate?: boolean;
  pending_update_count?: number;
  ip_address?: string;
  last_error_date?: number;
  last_error_message?: string;
  last_synchronization_error_date?: number;
  max_connections?: number;
  allowed_updates?: string[];
}

/** 读取后端 config */
export function useSettings(enabled = true) {
  return useQuery({
    queryKey: qk.settings.config(),
    queryFn: async () => (await api.get<SettingsConfig>("/settings")).data,
    enabled,
  });
}

/** 保存 config（仅本地后端配置：Token/ChatID 等；不含 Telegram setWebhook） */
export function useSaveSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (payload: SettingsConfig) => {
      // webhookUrl 不由 /settings 持久化，避免用户误以为已注册到 Telegram
      const { webhookUrl: _w, ...body } = payload;
      return (await api.post("/settings", body)).data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.settings.config() });
      qc.invalidateQueries({ queryKey: ["telegram", "verify"] });
      toast.success("设置已保存");
    },
    onError: (error: unknown) => toast.error(apiErrorText(error, "保存失败")),
  });
}

/** 向 Telegram 注册 Webhook（与「Telegram 下单」页同一接口） */
export function useSetTelegramWebhook() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (webhookUrl: string) => {
      const res = await api.post<{
        success?: boolean;
        error?: string;
        message?: string;
        webhook_url?: string;
        webhook_info?: TelegramWebhookInfo;
      }>("/telegram/set-webhook", { webhook_url: webhookUrl.trim() });
      if (res.data?.success === false) {
        throw new Error(res.data?.error || res.data?.message || "Webhook 设置失败");
      }
      return res.data;
    },
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: qk.settings.telegramWebhookInfo() });
      toast.success(data?.message || `Webhook 已注册：${data?.webhook_url || ""}`);
    },
    onError: (error: unknown) => toast.error(apiErrorText(error, "Webhook 设置失败")),
  });
}

/** 缓存信息 */
export function useCacheInfo() {
  return useQuery({
    queryKey: qk.settings.cacheInfo(),
    queryFn: async () => (await api.get("/cache/info")).data,
  });
}

/** Telegram Webhook 信息（按需触发，避免无 token 时报错） */
export function useTelegramWebhookInfo() {
  return useQuery({
    queryKey: qk.settings.telegramWebhookInfo(),
    queryFn: async () => {
      const res = await api.get<{ success: boolean; webhook_info?: TelegramWebhookInfo; error?: string }>(
        "/telegram/get-webhook-info"
      );
      if (!res.data?.success) throw new Error(res.data?.error || "获取 webhook 信息失败");
      return res.data.webhook_info || {};
    },
    enabled: false,
    retry: false,
  });
}

/** 清除缓存 */
export function useClearCache() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (type: "all" | "memory" | "sqlite") =>
      (await api.post("/cache/clear", { type })).data,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.settings.cacheInfo() });
      toast.success("已清除缓存");
    },
    onError: (error: unknown) => toast.error(apiErrorText(error, "清除失败")),
  });
}
