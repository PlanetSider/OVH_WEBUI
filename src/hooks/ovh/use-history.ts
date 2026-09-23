import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, apiErrorText } from "@/lib/http";
import { qk } from "@/lib/query";
import { toast } from "sonner";

export interface PurchaseHistory {
  id: string;
  accountId: string;
  /** 关联的抢购队列任务 ID（后端 PurchaseHistoryEntry.taskId） */
  taskId?: string;
  planCode: string;
  datacenter: string;
  options?: string[];
  status: "success" | "failed" | "uncertain";
  orderId?: string;
  orderUrl?: string;
  errorMessage?: string;
  purchaseTime: string;
  /** 抢购到这单时一共尝试了几次（后端 attemptCount） */
  attemptCount?: number;
  expirationTime?: string;
  price?: {
    withTax?: number;
    withoutTax?: number;
    tax?: number;
    currencyCode?: string;
  };
  /** OVH /me/order/{id}/status 的最近状态快照 */
  orderStatus?: string;
  orderStatusAt?: string;
  /** 本轮抢购已经完成的阶段墙钟耗时 */
  timing?: { name: string; ms: number }[];
  totalMs?: number;
}

/** 抢购历史 */
export function useHistory() {
  return useQuery({
    queryKey: qk.history(),
    queryFn: async () => (await api.get<PurchaseHistory[]>("/purchase-history")).data,
  });
}

export interface OrderStatusRefreshResult {
  success: boolean;
  status: "success" | "partial";
  message: string;
  candidates: number;
  selected: number;
  updated: number;
  skipped: number;
  failed: number;
  errors?: string[];
}

/** 手动刷新已有成功订单的 OVH 状态。 */
export function useRefreshOrderStatus() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () =>
      (await api.post<OrderStatusRefreshResult>("/purchase-history/refresh-status")).data,
    onSuccess: (result) => {
      qc.invalidateQueries({ queryKey: qk.history() });
      if (result.failed > 0) {
        toast.warning(result.message, { description: result.errors?.[0] });
      } else {
        toast.success(result.message);
      }
    },
    onError: (error: unknown) => toast.error(apiErrorText(error, "刷新订单状态失败")),
  });
}
/** 清空抢购历史 */
export function useClearHistory() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => (await api.delete("/purchase-history")).data,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.history() });
      toast.success("已清空购买历史");
    },
    onError: (error: unknown) => toast.error(apiErrorText(error, "清空失败")),
  });
}
