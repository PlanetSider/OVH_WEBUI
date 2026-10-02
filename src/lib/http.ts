/**
 * 统一 HTTP 客户端（唯一传输层）
 *
 * - 开发：Vite 代理 `/api`、`/health` → `http://127.0.0.1:19998`
 * - 生产：同源；localStorage `backendUrl` 可覆盖到独立后端
 * - 鉴权：`X-API-Key` + `X-Request-Time` + `X-Request-Nonce` + `X-Request-Signature`
 * - 多账户：`/server-control`、`/vps-control`、`/ovh/` 自动注入 `account`
 *
 * 用法：
 * - hooks / 页面：`import { api } from "@/lib/http"` → Axios，路径相对 `/api`
 * - 业务 facade：`import { apiRequest } from "@/lib/http"` 或 `import { api } from "@/lib/api"`
 */
import axios, {
  AxiosError,
  type AxiosInstance,
  type AxiosRequestConfig,
  type Method,
} from "axios";
import { hmac } from "@noble/hashes/hmac.js";
import { sha256 } from "@noble/hashes/sha2.js";
import { toast } from "sonner";

export function apiErrorText(error: unknown, fallback: string): string {
  const value = error as { response?: { data?: { error?: string; message?: string } }; message?: string };
  return value.response?.data?.error || value.response?.data?.message || value.message || fallback;
}

// ─── storage keys ───────────────────────────────────────────

export const API_KEY_STORAGE = "ovh_sniper_api_key";
export const BACKEND_URL_STORAGE = "backendUrl";
export const SERVER_CONTROL_ACCOUNT_KEY = "ovh_active_server_control_account_id";

// ─── storage helpers ────────────────────────────────────────

export function getApiSecretKey(): string {
  if (typeof window === "undefined") return "";
  return (
    window.localStorage.getItem(API_KEY_STORAGE) ||
    window.localStorage.getItem("apiSecretKey") ||
    ""
  );
}

export function setApiSecretKey(key: string): void {
  window.localStorage.setItem(API_KEY_STORAGE, key);
  window.localStorage.setItem("apiSecretKey", key);
}

export function clearApiSecretKey(): void {
  window.localStorage.removeItem(API_KEY_STORAGE);
  window.localStorage.removeItem("apiSecretKey");
}

/** 空字符串 = 同源 / Vite 代理 */
export function getBackendUrl(): string {
  if (typeof window === "undefined") return "";
  return window.localStorage.getItem(BACKEND_URL_STORAGE) || "";
}

export function setBackendUrl(url: string): void {
  const v = url.trim().replace(/\/$/, "");
  if (v) {
    window.localStorage.setItem(BACKEND_URL_STORAGE, v);
  } else {
    window.localStorage.removeItem(BACKEND_URL_STORAGE);
  }
}

export function getActiveServerControlAccount(): string {
  if (typeof window === "undefined") return "";
  return window.localStorage.getItem(SERVER_CONTROL_ACCOUNT_KEY) || "";
}

export function setActiveServerControlAccount(id: string): void {
  if (id) {
    window.localStorage.setItem(SERVER_CONTROL_ACCOUNT_KEY, id);
  } else {
    window.localStorage.removeItem(SERVER_CONTROL_ACCOUNT_KEY);
  }
  window.dispatchEvent(new Event("ovh-active-account-changed"));
}

// ─── base URL ───────────────────────────────────────────────

/** Axios 业务请求的 baseURL：`/api` 或 `http://host:port/api` */
export function resolveApiBaseURL(): string {
  const origin = getBackendUrl().replace(/\/$/, "");
  return origin ? `${origin}/api` : "/api";
}

/** 非 /api 路径（如 /health）的完整 URL */
export function resolveAbsoluteUrl(path: string): string {
  const origin = getBackendUrl().replace(/\/$/, "");
  if (path.startsWith("http")) return path;
  const p = path.startsWith("/") ? path : `/${path}`;
  return origin ? `${origin}${p}` : p;
}

// ─── Axios instance ─────────────────────────────────────────

type ExtraConfig = AxiosRequestConfig & {
  /** 为 true 时 401 不弹 toast（健康检查等） */
  silent401?: boolean;
  /** 为 false 时不自动注入 account（默认 true） */
  injectAccount?: boolean;
  /**
   * 为 true 时不套 `/api` baseURL（用于 /health 或完整绝对 URL）。
   * url 应以 `/` 或 `http` 开头。
   */
  absolute?: boolean;
};

function requestPath(config: AxiosRequestConfig): string {
  try {
    const raw = axios.getUri(config);
    if (typeof window !== "undefined") {
      const parsed = new URL(raw, window.location.origin);
      return `${parsed.pathname}${parsed.search}`;
    }
    return raw;
  } catch {
    const base = config.baseURL || "";
    const raw = `${base}${config.url || ""}`;
    if (typeof window !== "undefined") {
      try {
        const parsed = new URL(raw, window.location.origin);
        return `${parsed.pathname}${parsed.search}`;
      } catch {
        // fall through to the conservative relative-path form
      }
    }
    const path = config.url || "/";
    if (path.startsWith("/api/")) return path;
    if (path === "/api") return "/api";
    return `${base.replace(/\/$/, "")}/${path.replace(/^\//, "")}` || "/";
  }
}

function requestBodyBytes(config: AxiosRequestConfig): Uint8Array {
  if (config.data == null || config.data === "") return new Uint8Array();
  if (typeof config.data === "string") return new TextEncoder().encode(config.data);
  return new TextEncoder().encode(JSON.stringify(config.data));
}

function browserCrypto(): Crypto {
  if (typeof window === "undefined" || !window.crypto) {
    throw new Error("Secure browser crypto is unavailable");
  }
  return window.crypto;
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("");
}

function createSecureNonce(): string {
  const cryptoApi = browserCrypto();
  if (typeof cryptoApi.getRandomValues !== "function") {
    throw new Error("Secure random number generation is unavailable");
  }
  return bytesToHex(cryptoApi.getRandomValues(new Uint8Array(16)));
}

function signingPayload(method: string, path: string, timestamp: string, nonce: string, body: Uint8Array): Uint8Array {
  const prefix = new TextEncoder().encode(`${method.toUpperCase()}\n${path}\n${timestamp}\n${nonce}\n`);
  const payload = new Uint8Array(prefix.length + body.length);
  payload.set(prefix);
  payload.set(body, prefix.length);
  return payload;
}

async function signRequest(apiKey: string, method: string, path: string, timestamp: string, nonce: string, body: Uint8Array): Promise<string> {
  const cryptoApi = browserCrypto();
  const keyBytes = new TextEncoder().encode(apiKey);
  const payload = signingPayload(method, path, timestamp, nonce, body);
  if (typeof cryptoApi.subtle?.importKey === "function" && typeof cryptoApi.subtle.sign === "function") {
    const cryptoKey = await cryptoApi.subtle.importKey(
      "raw",
      keyBytes,
      { name: "HMAC", hash: "SHA-256" },
      false,
      ["sign"],
    );
    const digest = await cryptoApi.subtle.sign("HMAC", cryptoKey, payload);
    return bytesToHex(new Uint8Array(digest));
  }
  return bytesToHex(hmac(sha256, keyBytes, payload));
}

export async function buildRequestAuthHeaders(apiKey: string, method: string, path: string, body: string | Uint8Array = ""): Promise<Record<string, string>> {
  const timestamp = Date.now().toString();
  const nonce = createSecureNonce();
  const bodyBytes = typeof body === "string" ? new TextEncoder().encode(body) : body;
  return {
    "X-API-Key": apiKey,
    "X-Request-Time": timestamp,
    "X-Request-Nonce": nonce,
    "X-Request-Signature": await signRequest(apiKey, method, path, timestamp, nonce, bodyBytes),
  };
}

function createApiClient(): AxiosInstance {
  const client = axios.create({
    baseURL: "/api",
    timeout: 120000,
  });

  client.interceptors.request.use(async (config) => {
    const extra = config as ExtraConfig;
    const url = config.url || "";

    // 绝对 URL：清空 base；apiRequest(/health)：absolute 标记；其余走 /api base
    if (url.startsWith("http")) {
      config.baseURL = undefined;
    } else if (extra.absolute) {
      config.baseURL = "";
    } else {
      // 每次请求解析 backendUrl，改 localStorage 后无需重建客户端
      config.baseURL = resolveApiBaseURL();
    }

    if (extra.injectAccount !== false) {
      // 相对 /api 的路径，或绝对 URL 中含控制/账户段
      const needAccount =
        url.includes("/server-control") ||
        url.includes("/vps-control") ||
        url.includes("/ovh/") ||
        url.startsWith("server-control") ||
        url.startsWith("vps-control") ||
        url.startsWith("ovh/");

      if (needAccount && !(config.params && (config.params as Record<string, unknown>).account)) {
        const acc = getActiveServerControlAccount();
        if (acc) {
          config.params = { ...(config.params || {}), account: acc };
        }
      }
    }

    const key = getApiSecretKey();
    const path = requestPath(config);
    if (key && path.startsWith("/api/")) {
      const body = requestBodyBytes(config);
      if (body.length > 0 && !config.headers.get("Content-Type")) {
        config.headers.set("Content-Type", "application/json");
      }
      if (typeof config.data !== "string" && config.data != null) {
        config.data = JSON.stringify(config.data);
      }
      const authHeaders = await buildRequestAuthHeaders(key, config.method || "GET", path, body);
      for (const [header, value] of Object.entries(authHeaders)) {
        config.headers.set(header, value);
      }
    }
    return config;
  });

  client.interceptors.response.use(
    (res) => res,
    (error: AxiosError<{ error?: string; message?: string }>) => {
      const silent = (error.config as ExtraConfig | undefined)?.silent401;
      if (error.response?.status === 401 && !silent) {
        toast.error("身份验证失败，请检查 API 设置");
      }
      return Promise.reject(error);
    }
  );

  return client;
}

/** Axios 实例：hooks 使用，路径相对 `/api`（如 `/servers`、`/monitor/status`） */
export const api = createApiClient();
export default api;

// ─── fetch 风格 facade（供 lib/api.ts 业务方法） ────────────

export class ApiError extends Error {
  status: number;
  data: unknown;
  constructor(message: string, status: number, data?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.data = data;
  }
}

/**
 * 统一请求入口（返回 JSON body，不包 AxiosResponse）。
 * - 路径写完整 `/api/...` 或 `/health`
 * - body 可用 `JSON.stringify` 的 string（与旧 fetch 调用兼容）
 */
export async function apiRequest<T = unknown>(
  endpoint: string,
  options: RequestInit = {},
  opts?: { account?: boolean; silent401?: boolean }
): Promise<T> {
  const method = ((options.method || "GET").toUpperCase() || "GET") as Method;
  let data: unknown = undefined;
  if (options.body != null && options.body !== "") {
    if (typeof options.body === "string") {
      try {
        data = JSON.parse(options.body);
      } catch {
        data = options.body;
      }
    } else {
      data = options.body;
    }
  }

  // 解析路径：/api/xxx → base=/api path=/xxx；/health → absolute URL
  let url = endpoint;
  let absolute = false;

  if (endpoint.startsWith("http")) {
    absolute = true;
  } else if (endpoint === "/health" || endpoint.startsWith("/health?")) {
    url = resolveAbsoluteUrl(endpoint);
    absolute = true;
  } else if (endpoint.startsWith("/api/") || endpoint === "/api") {
    url = endpoint === "/api" ? "/" : endpoint.slice("/api".length) || "/";
  } else if (!endpoint.startsWith("/")) {
    url = "/" + endpoint;
  }

  // 合并调用方 headers（少见）
  const headers: Record<string, string> = {};
  if (options.headers) {
    const h = new Headers(options.headers as HeadersInit);
    h.forEach((v, k) => {
      headers[k] = v;
    });
  }

  const config: ExtraConfig = {
    url,
    method,
    data,
    signal: options.signal ?? undefined,
    headers,
    silent401: opts?.silent401,
    injectAccount: opts?.account !== false,
    absolute,
  };

  try {
    const res = await api.request<T>(config);
    if (res.status === 204) return undefined as T;
    return res.data;
  } catch (err) {
    if (axios.isCancel(err)) throw err;
    const ax = err as AxiosError<{ message?: string; error?: string }>;
    const status = ax.response?.status ?? 0;
    const errorData = ax.response?.data ?? {};
    const msg =
      (errorData as { message?: string }).message ||
      (errorData as { error?: string }).error ||
      ax.message ||
      (status ? `HTTP ${status}` : "网络错误");
    throw new ApiError(msg, status, errorData);
  }
}
