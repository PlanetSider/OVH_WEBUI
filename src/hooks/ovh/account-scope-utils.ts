import type { QueryKey } from "@tanstack/react-query";
import type { AxiosRequestConfig } from "axios";

export function accountQueryKey(base: QueryKey, accountId: string): QueryKey {
  return [...base, accountId];
}

export function accountRequestConfig(
  accountId: string,
  config?: AxiosRequestConfig,
): AxiosRequestConfig & { injectAccount: false } {
  if (!accountId) throw new Error("请先选择 OVH 账户");
  return {
    ...config,
    params: { ...config?.params, account: accountId },
    injectAccount: false,
  };
}
