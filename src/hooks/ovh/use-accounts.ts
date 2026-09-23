import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, apiErrorText, getActiveServerControlAccount, setActiveServerControlAccount } from "@/lib/http";
import { qk } from "@/lib/query";
import { toast } from "sonner";

export interface OVHAccount {
  id: string;
  name: string;
  endpoint: string;
  zone: string;
  appKey?: string;
  appSecret?: string;
  consumerKey?: string;
  iam: string;
  proxyUrl?: string;
  fingerprint?: string;
  isDefault: boolean;
  createdAt: string;
}

export interface AccountInput {
  name: string;
  zone: string;
  endpoint?: string; // 可空, 后端按 zone 推
  appKey: string;
  appSecret: string;
  consumerKey: string;
  iam?: string;
  proxyUrl?: string;
  fingerprint?: string;
  clearProxy?: boolean;
  clearFingerprint?: boolean;
  setDefault?: boolean;
}

const ACCOUNTS_KEY = ["accounts", "list"] as const;

export interface AccountStatus {
  id: string;
  name: string;
  alias?: string;
  zone?: string;
  email?: string;
  valid: boolean;
  error?: string;
}

export function useAccountStatuses(enabled = false) {
  return useQuery({
    queryKey: ["accounts", "status"],
    queryFn: async () => (await api.get<{ accounts: AccountStatus[] }>("/accounts/status")).data.accounts || [],
    enabled,
    staleTime: 30_000,
  });
}

/** 全部账户列表(默认账户排首位) */
export function useAccounts() {
  return useQuery({
    queryKey: ACCOUNTS_KEY,
    queryFn: async () => {
      const res = await api.get<{ accounts: OVHAccount[] }>("/accounts");
      return res.data.accounts || [];
    },
    staleTime: 5 * 60_000,
  });
}

/** 默认账户(取列表中 isDefault, 没有就第一个) */
export function useDefaultAccount(): OVHAccount | null {
  const q = useAccounts();
  const list = q.data || [];
  return list.find((a) => a.isDefault) || list[0] || null;
}

/** 创建账户。后端会自动调 /me 验证,返回 valid 字段 */
export function useCreateAccount() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: AccountInput) => {
      const res = await api.post<{ account: OVHAccount; valid: boolean }>("/accounts", input);
      return res.data;
    },
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ACCOUNTS_KEY });
      qc.invalidateQueries({ queryKey: qk.accounts.proxyStatus() });
      // 新账户立即设为活跃，避免 localStorage 仍指向旧 ID
      if (data?.account?.id) {
        setActiveServerControlAccount(data.account.id);
        void qc.invalidateQueries({ queryKey: ["server-control"] });
        void qc.invalidateQueries({ queryKey: ["vps-control"] });
        void qc.invalidateQueries({ queryKey: ["account"] });
      }
      if (data.valid) {
        toast.success(`账户 ${data.account.name} 创建成功`);
      } else {
        toast.warning(`账户创建失败或 OVH 验证未通过，请检查凭据`);
      }
    },
    onError: (error: unknown) => toast.error(apiErrorText(error, "创建失败")),
  });
}

/** 更新账户 */
export function useUpdateAccount() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, input }: { id: string; input: Partial<AccountInput> }) => {
      const res = await api.put<{ account: OVHAccount; valid: boolean }>(`/accounts/${id}`, input);
      return res.data;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ACCOUNTS_KEY });
      qc.invalidateQueries({ queryKey: qk.accounts.proxyStatus() });
      toast.success("账户已更新");
    },
    onError: (error: unknown) => toast.error(apiErrorText(error, "更新失败")),
  });
}

/** 删除账户(级联删除关联的 queue/history/sniper 记录) */
export function useDeleteAccount() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      await api.delete(`/accounts/${id}`);
      return id;
    },
    onSuccess: (id) => {
      if (getActiveServerControlAccount() === id) {
        setActiveServerControlAccount("");
      }
      qc.invalidateQueries({ queryKey: ACCOUNTS_KEY });
      qc.invalidateQueries({ queryKey: qk.accounts.proxyStatus() });
      qc.invalidateQueries({ queryKey: ["queue"] });
      qc.invalidateQueries({ queryKey: ["history"] });
      qc.invalidateQueries({ queryKey: ["server-control"] });
      qc.invalidateQueries({ queryKey: ["vps-control"] });
      qc.invalidateQueries({ queryKey: ["account"] });
      toast.success("账户已删除,关联数据一并清理");
    },
    onError: (error: unknown) => toast.error(apiErrorText(error, "删除失败")),
  });
}

/** 把指定账户标为默认 */
export function useSetDefaultAccount() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => (await api.post(`/accounts/${id}/set-default`)).data,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ACCOUNTS_KEY });
      qc.invalidateQueries({ queryKey: qk.accounts.proxyStatus() });
      toast.success("已设为默认账户");
    },
    onError: (error: unknown) => toast.error(apiErrorText(error, "设默认失败")),
  });
}

/** 重新验证账户凭据(调 OVH /me) */
export function useVerifyAccount() {
  return useMutation({
    mutationFn: async (id: string) =>
      (await api.post<{ valid: boolean }>(`/accounts/${id}/verify`)).data,
    onSuccess: (data) => {
      if (data.valid) {
        toast.success("OVH 凭据验证通过");
      } else {
        toast.error("OVH 凭据验证失败,检查 AppKey / AppSecret / ConsumerKey");
      }
    },
  });
}

/** 按 ID 查账户(从 useAccounts 缓存里找,不发请求) */
export function findAccountByID(accounts: OVHAccount[] | undefined, id: string): OVHAccount | undefined {
  if (!accounts || !id) return undefined;
  return accounts.find((a) => a.id === id);
}

/** zone 颜色映射, 用于账户 chip 区分(EU 蓝 / US 红 / CA 绿 等) */
export function accountChipColor(zone: string): string {
  const z = zone.toUpperCase();
  if (z === "US") return "bg-red-100 text-red-700 dark:bg-red-950/40 dark:text-red-300";
  if (z === "CA" || z === "QC") return "bg-green-100 text-green-700 dark:bg-green-950/40 dark:text-green-300";
  if (z === "ASIA" || z === "SG" || z === "AU" || z === "IN") return "bg-orange-100 text-orange-700 dark:bg-orange-950/40 dark:text-orange-300";
  // EU 系
  return "bg-blue-100 text-blue-700 dark:bg-blue-950/40 dark:text-blue-300";
}

// ─── 出站代理 / 指纹诊断 ───────────────────────────────────────────────────

export interface AccountProxyStatus {
  id: string;
  name: string;
  zone: string;
  usingProxy: boolean;
  proxy: string;
  fingerprint: string;
  tripped: boolean;
  fails: number;
  trippedAt?: string;
  lastFailAt?: string;
}

export interface ProxyStatusResult {
  success: boolean;
  profiles: string[];
  accounts: AccountProxyStatus[];
}

export interface ProxyTestSuccess {
  success: true;
  egressIP: string;
  usingProxy: boolean;
  proxy: string;
  fingerprint: string;
  warning?: string;
}

export interface ProxyTestFailure {
  success: false;
  error: string;
  via: string;
  usingProxy: boolean;
  fingerprint: string;
}

export type ProxyTestResult = ProxyTestSuccess | ProxyTestFailure;
export type ProxyTestRecord = (ProxyTestSuccess | ProxyTestFailure) & { testedAt: number };

export interface ProxyProbeTarget {
  name: string;
  url: string;
  ok: boolean;
  status?: number;
  minMs?: number;
  avgMs?: number;
  error?: string;
}

export interface ProxyCheckSuccess {
  success: true;
  accountId: string;
  accountName: string;
  region: string;
  usingProxy: boolean;
  proxy: string;
  fingerprint: string;
  egressIP?: string;
  egressError?: string;
  checkedAt: string;
  warning?: string;
  targets: ProxyProbeTarget[];
}

export interface ProxyCheckFailure {
  success: false;
  error: string;
}

export type ProxyCheckResult = ProxyCheckSuccess | ProxyCheckFailure;
export type ProxyCheckRecord = (ProxyCheckSuccess | ProxyCheckFailure) & { receivedAt: number };

export function useProxyStatus() {
  return useQuery<ProxyStatusResult>({
    queryKey: qk.accounts.proxyStatus(),
    queryFn: async () => (await api.get<ProxyStatusResult>("/accounts/proxy-status")).data,
    refetchInterval: 30_000,
  });
}

export function useProxyTest() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => (await api.post<ProxyTestResult>(`/accounts/${id}/proxy-test`)).data,
    onSuccess: (data, id) => {
      qc.setQueryData(qk.accounts.proxyTest(id), { ...data, testedAt: Date.now() } satisfies ProxyTestRecord);
      if (data.success === true) {
        toast.success(`出口 IP ${data.egressIP}${data.usingProxy ? "（经代理）" : "（直连）"}`);
        if (data.warning) toast.warning(data.warning, { duration: 8000 });
      } else {
        toast.error(`出口测试失败（${data.via}）：${data.error}`);
      }
    },
    onError: (error: unknown) => toast.error(apiErrorText(error, "出口测试请求失败")),
  });
}

export function useLastProxyTest(accountId: string) {
  return useQuery<ProxyTestRecord | null>({
    queryKey: qk.accounts.proxyTest(accountId),
    queryFn: async () => null,
    enabled: false,
    staleTime: Infinity,
  });
}

export function useProxyCheck() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => (await api.post<ProxyCheckResult>(`/accounts/${id}/proxy-check`)).data,
    onSuccess: (data, id) => {
      qc.setQueryData(qk.accounts.proxyCheck(id), { ...data, receivedAt: Date.now() } satisfies ProxyCheckRecord);
      if (data.success === false) {
        toast.error(`链路检测失败：${data.error}`);
        return;
      }
      const down = data.targets.filter((target) => !target.ok);
      if (down.length > 0) {
        toast.error(`${down.length}/${data.targets.length} 个目标不通`);
        return;
      }
      const latencies = data.targets.filter((target) => target.ok && typeof target.minMs === "number").map((target) => target.minMs as number);
      const worst = latencies.length ? Math.max(...latencies) : undefined;
      if (data.warning) toast.warning(data.warning, { duration: 8000 });
      if (worst === undefined) toast.warning("链路已返回，但没有可用延迟样本");
      else if (worst > 800) toast.error(`链路检测完成，最慢目标 ${worst}ms`);
      else if (worst > 300) toast.warning(`链路检测完成，最慢目标 ${worst}ms`);
      else toast.success(`链路检测完成，最慢目标 ${worst}ms`);
    },
    onError: (error: unknown) => toast.error(apiErrorText(error, "链路检测请求失败")),
  });
}

export function useLastProxyCheck(accountId: string) {
  return useQuery<ProxyCheckRecord | null>({
    queryKey: qk.accounts.proxyCheck(accountId),
    queryFn: async () => null,
    enabled: false,
    staleTime: Infinity,
  });
}
