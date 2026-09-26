export const MAX_QUEUE_BATCH_TASKS = 200;

export function resolveImportedQueueOptions<T extends string>(
  imported: string,
  groups: Record<T, readonly { value: string }[]> | null,
): { picked: Partial<Record<T, string>>; extras: string[] } {
  const wanted = [...new Set(imported.split(",").map((value) => value.trim()).filter(Boolean))];
  const consumed = new Set<string>();
  const picked: Partial<Record<T, string>> = {};
  if (groups) {
    for (const key of Object.keys(groups) as T[]) {
      const match = groups[key].find((option) => wanted.includes(option.value));
      if (match) {
        picked[key] = match.value;
        consumed.add(match.value);
      }
    }
  }
  return { picked, extras: wanted.filter((value) => !consumed.has(value)) };
}

export function mergeQueueOptions<T extends string>(picked: Partial<Record<T, string>>, extras: string): string[] {
  return [...new Set([
    ...Object.values(picked).filter((value): value is string => typeof value === "string" && value.length > 0),
    ...extras.split(",").map((value) => value.trim()).filter(Boolean),
  ])];
}

export function isValidQueueBatch(quantity: number, datacenterCount: number): boolean {
  return Number.isSafeInteger(quantity)
    && quantity > 0
    && Number.isSafeInteger(datacenterCount)
    && datacenterCount > 0
    && datacenterCount <= MAX_QUEUE_BATCH_TASKS
    && quantity <= Math.floor(MAX_QUEUE_BATCH_TASKS / datacenterCount);
}

// Keep the UI preflight aligned with catalog.SubsidiaryOfAccount in the Go backend.
export function subsidiaryForQueueAccount(account: { zone?: string; endpoint?: string } | null | undefined): string | null {
  if (!account) return null;
  const zone = account.zone?.trim().toUpperCase();
  if (zone) return zone;
  switch (account.endpoint?.trim().toLowerCase()) {
    case "ovh-us":
      return "US";
    case "ovh-ca":
    case "kimsufi-ca":
    case "soyoustart-ca":
      return "CA";
    default:
      return "IE";
  }
}
