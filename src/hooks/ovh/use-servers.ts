import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/http";
import { qk } from "@/lib/query";
import { toast } from "sonner";

export interface ServerOption {
  label: string;
  value: string;
  family?: string;
}

export interface ServerPlan {
  planCode: string;
  name: string;
  description?: string;
  cpu: string;
  memory: string;
  storage: string;
  bandwidth: string;
  vrackBandwidth: string;
  defaultOptions: ServerOption[];
  availableOptions: ServerOption[];
  datacenters: {
    datacenter: string;
    dcName: string;
    region: string;
    availability: string;
    countryCode: string;
  }[];
}

/** 服务器目录（带可用性）。后端整点刷新完整目录；页面每 5 分钟轻量读取
 * 一次后端缓存，以便已经打开的页面及时看到新批次。 */
export function useServers(showApiServers: boolean = true) {
  const qc = useQueryClient();
  const key = qk.servers.list(showApiServers);
  const q = useQuery({
    queryKey: key,
    queryFn: async () => {
      const res = await api.get("/servers", { params: { showApiServers } });
      return (res.data.servers || res.data || []) as ServerPlan[];
    },
    staleTime: 2 * 60 * 60_000,
    refetchInterval: 5 * 60_000,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });

  const refresh = useMutation({
    mutationFn: async () => {
      const res = await api.get("/servers", {
        params: { showApiServers, forceRefresh: true },
      });
      return (res.data.servers || res.data || []) as ServerPlan[];
    },
    onSuccess: (servers) => {
      qc.setQueryData(key, servers);
      qc.invalidateQueries({ queryKey: ["settings", "cache-info"] });
    },
  });

  const forceRefresh = async () => {
    await refresh.mutateAsync();
  };

  return Object.assign(q, {
    forceRefresh,
    isRefreshing: refresh.isPending,
  });
}

/** 添加到监控订阅 */
export function useAddToMonitor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (payload: { planCode: string; datacenters: string[]; serverName?: string }) =>
      (await api.post("/monitor/subscriptions", { ...payload, notifyAvailable: true, notifyUnavailable: false })).data,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.monitor.list() });
      toast.success("已加入监控");
    },
    onError: (e: any) =>
      toast.error(
        e.response?.data?.message || e.response?.data?.error || "加入监控失败"
      ),
  });
}
