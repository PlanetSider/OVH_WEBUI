import { useMemo } from "react";
import { useQuery, type QueryKey, type UseQueryOptions } from "@tanstack/react-query";
import type { AxiosInstance, AxiosRequestConfig } from "axios";
import { api } from "@/lib/http";
import { accountQueryKey, accountRequestConfig } from "@/hooks/ovh/account-scope-utils";
import { useActiveServerControlAccount } from "@/hooks/ovh/use-active-account";

type ScopedMethods = Pick<AxiosInstance, "get" | "delete" | "post" | "put" | "patch">;

/** Bind a control request to the account visible when its hook rendered. */
export function useScopedAccountApi(): ScopedMethods & { accountId: string } {
  const [accountId] = useActiveServerControlAccount();
  return useMemo(() => {
    const withAccount = (config?: AxiosRequestConfig) => accountRequestConfig(accountId, config);
    return {
      accountId,
      get: (url: string, config?: AxiosRequestConfig) => api.get(url, withAccount(config)),
      delete: (url: string, config?: AxiosRequestConfig) => api.delete(url, withAccount(config)),
      post: (url: string, data?: unknown, config?: AxiosRequestConfig) => api.post(url, data, withAccount(config)),
      put: (url: string, data?: unknown, config?: AxiosRequestConfig) => api.put(url, data, withAccount(config)),
      patch: (url: string, data?: unknown, config?: AxiosRequestConfig) => api.patch(url, data, withAccount(config)),
    } as ScopedMethods & { accountId: string };
  }, [accountId]);
}

/** Scope cached control/account data to the same identity as its network request. */
export function useAccountQuery<TQueryFnData, TError = Error, TData = TQueryFnData>(
  accountId: string,
  options: UseQueryOptions<TQueryFnData, TError, TData, QueryKey>,
) {
  return useQuery({
    ...options,
    queryKey: accountQueryKey(options.queryKey, accountId),
    enabled: !!accountId && (options.enabled ?? true),
  });
}
