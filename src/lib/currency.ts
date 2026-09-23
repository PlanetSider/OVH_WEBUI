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
