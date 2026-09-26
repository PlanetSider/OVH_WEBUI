import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useAccountQuery, useScopedAccountApi } from "@/hooks/ovh/use-account-scope";
import { toast } from "sonner";

/** 服务器本地别名 map: { service_name: alias }。
 *  读取与写入都绑定当前账户；空别名表示删除该账户的记录。
 */
export function useServerAliases() {
  const api = useScopedAccountApi();
  return useAccountQuery<Record<string, string>>(api.accountId, {
    queryKey: ["server-control", "aliases"],
    queryFn: async () => (await api.get<Record<string, string>>("/server-control/aliases")).data,
    staleTime: 30 * 60_000,
    gcTime: 60 * 60_000,
    refetchOnWindowFocus: false,
  });
}

/** 设置 / 删除一台机器的别名 */
export function useSetServerAlias() {
  const api = useScopedAccountApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ serviceName, alias }: { serviceName: string; alias: string }) => {
      const trimmed = alias.trim();
      if (trimmed === "") {
        await api.delete(`/server-control/${encodeURIComponent(serviceName)}/alias`);
      } else {
        await api.put(`/server-control/${encodeURIComponent(serviceName)}/alias`, { alias: trimmed });
      }
      return { serviceName, alias: trimmed };
    },
    onSuccess: ({ alias }) => {
      qc.invalidateQueries({ queryKey: ["server-control", "aliases"] });
      toast.success(alias === "" ? "已清除别名" : "别名已保存");
    },
    onError: (e: any) => {
      toast.error(e?.response?.data?.error || "保存失败");
    },
  });
}

/** 显示用:有别名取别名,没别名取原名(通常是 service_name 或 commercial display name) */
export function aliasOf(aliases: Record<string, string> | undefined, serviceName: string, fallback: string): string {
  const a = aliases?.[serviceName];
  return a && a !== "" ? a : fallback;
}
