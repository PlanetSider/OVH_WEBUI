const CURRENCY_SYMBOLS: Record<string, string> = {
  EUR: "€",
  USD: "$",
  CAD: "CA$",
  GBP: "£",
  AUD: "A$",
  SGD: "S$",
  INR: "₹",
  PLN: "zł",
  JPY: "¥",
  CNY: "¥",
  KRW: "₩",
  HKD: "HK$",
};

/** 只接受 API 明确返回的三字母币种代码，不为缺失值推导默认币种。 */
export function normalizeCurrencyCode(value: unknown): string {
  const code = String(value ?? "").trim().toUpperCase();
  return /^[A-Z]{3}$/.test(code) ? code : "";
}

/** 用于文字标签的币种名称；未知值不能伪装成 EUR/USD。 */
export function currencyLabel(value: unknown): string {
  return normalizeCurrencyCode(value) || "币种未知";
}

/** 金额格式化：已知币种显示符号，未知币种保留金额并明确提示。 */
export function formatCurrencyAmount(value: number, currency: unknown, fractionDigits = 2): string {
  if (!Number.isFinite(value)) return "—";
  const code = normalizeCurrencyCode(currency);
  const amount = value.toFixed(fractionDigits);
  if (!code) return `${amount}（币种未知）`;
  const symbol = CURRENCY_SYMBOLS[code];
  return symbol ? `${symbol}${amount}` : `${code} ${amount}`;
}

export type AccountRegion = "CA" | "US" | "IE" | "OTHER";

export interface ExchangeDisplaySettings {
  exchangeDisplayMode?: "original" | "cny";
  exchangeActive?: boolean;
  exchangeEurCny?: number;
  exchangeUsdCny?: number;
  exchangeCadCny?: number;
}

export interface DisplayPrice {
  amount: number;
  currency: string;
  converted: boolean;
}

export function classifyAccountRegion(zone: unknown): AccountRegion {
  const normalized = String(zone ?? "").trim().toUpperCase();
  if (normalized === "CA" || normalized === "QC") return "CA";
  if (normalized === "US") return "US";
  if (normalized === "IE") return "IE";
  return "OTHER";
}

/** CA/US 对外统一显示 USD，IE 统一显示 EUR；其他历史地区保留源币种。 */
export function targetCurrencyForAccount(zone: unknown, sourceCurrency: unknown): string {
  switch (classifyAccountRegion(zone)) {
    case "CA":
    case "US":
      return "USD";
    case "IE":
      return "EUR";
    default:
      return normalizeCurrencyCode(sourceCurrency);
  }
}

function cnyRate(currency: string, settings: ExchangeDisplaySettings): number | undefined {
  switch (normalizeCurrencyCode(currency)) {
    case "CNY": return 1;
    case "EUR": return settings.exchangeEurCny;
    case "USD": return settings.exchangeUsdCny;
    case "CAD": return settings.exchangeCadCny;
    default: return undefined;
  }
}

/**
 * 计算展示金额但不改变后端原始金额。CNY 模式只在后端汇率状态 active
 * 且对应缓存存在时转换；失败由调用方展示“不可用”，绝不回退到当前原币。
 */
export function resolveDisplayPrice(
  amount: number,
  sourceCurrency: unknown,
  accountZone: unknown,
  settings: ExchangeDisplaySettings,
): DisplayPrice | null {
  if (!Number.isFinite(amount)) return null;
  const source = normalizeCurrencyCode(sourceCurrency);
  if (!source) return { amount, currency: "", converted: false };
  if (settings.exchangeDisplayMode === "cny") {
    if (!settings.exchangeActive) return null;
    const fromCny = cnyRate(source, settings);
    if (!fromCny || fromCny <= 0) return null;
    return { amount: amount * fromCny, currency: "CNY", converted: source !== "CNY" };
  }
  const target = targetCurrencyForAccount(accountZone, source);
  if (target === source) return { amount, currency: source, converted: false };
  const fromCny = cnyRate(source, settings);
  const toCny = cnyRate(target, settings);
  if (fromCny && toCny && fromCny > 0 && toCny > 0) {
    return { amount: amount * fromCny / toCny, currency: target, converted: true };
  }
  // 目标币种已知但当前汇率未准备好时不显示原始金额，避免错标成 USD/EUR。
  return null;
}

export function formatDisplayPrice(
  amount: number,
  sourceCurrency: unknown,
  accountZone: unknown,
  settings: ExchangeDisplaySettings,
  fractionDigits = 2,
): string {
  const resolved = resolveDisplayPrice(amount, sourceCurrency, accountZone, settings);
  if (!resolved) return "价格不可用";
  return formatCurrencyAmount(resolved.amount, resolved.currency || sourceCurrency, fractionDigits);
}
