export const AI_MODELS_CACHE_KEY = 'shell:ai-models-cache:v1';

export type AIModelCacheMode = 'api' | 'cli';

export interface AIModelsCache {
  api: Record<string, string[]>;
  cli: Record<string, string[]>;
}

export interface StorageLike {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export function normalizeEndpoint(endpoint: string): string {
  return endpoint.trim().replace(/\/+$/, '');
}

export function normalizeCliCommand(command: string): string {
  return command.trim().toLowerCase();
}

export function normalizeModelCacheKey(mode: AIModelCacheMode, rawKey: string): string {
  return mode === 'api' ? normalizeEndpoint(rawKey) : normalizeCliCommand(rawKey);
}

function sanitizeModels(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const item of value) {
    if (typeof item !== 'string') continue;
    const trimmed = item.trim();
    if (!trimmed || seen.has(trimmed)) continue;
    seen.add(trimmed);
    out.push(trimmed);
  }
  return out;
}

function sanitizeBucket(value: unknown, mode: AIModelCacheMode): Record<string, string[]> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {};
  const out: Record<string, string[]> = {};
  for (const [rawKey, rawModels] of Object.entries(value as Record<string, unknown>)) {
    const key = normalizeModelCacheKey(mode, rawKey);
    const models = sanitizeModels(rawModels);
    if (key && models.length > 0) {
      out[key] = models;
    }
  }
  return out;
}

function resolveStorage(storage?: StorageLike): StorageLike | null {
  if (storage) return storage;
  try {
    return typeof globalThis !== 'undefined' && 'localStorage' in globalThis
      ? (globalThis.localStorage ?? null)
      : null;
  } catch {
    return null;
  }
}

export function createEmptyAIModelsCache(): AIModelsCache {
  return { api: {}, cli: {} };
}

export function loadAIModelsCache(storage?: StorageLike): AIModelsCache {
  const store = resolveStorage(storage);
  if (!store) return createEmptyAIModelsCache();
  try {
    const raw = store.getItem(AI_MODELS_CACHE_KEY);
    if (!raw) return createEmptyAIModelsCache();
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return createEmptyAIModelsCache();
    }
    const obj = parsed as Record<string, unknown>;
    return {
      api: sanitizeBucket(obj.api, 'api'),
      cli: sanitizeBucket(obj.cli, 'cli'),
    };
  } catch {
    return createEmptyAIModelsCache();
  }
}

export function saveAIModelsCache(cache: AIModelsCache, storage?: StorageLike): void {
  const store = resolveStorage(storage);
  if (!store) return;
  try {
    store.setItem(AI_MODELS_CACHE_KEY, JSON.stringify(cache));
  } catch {
    // Ignore storage quota / privacy mode errors gracefully.
  }
}

export function getCachedAIModels(
  cache: AIModelsCache,
  mode: AIModelCacheMode,
  rawKey: string,
): string[] | null {
  const key = normalizeModelCacheKey(mode, rawKey);
  if (!key) return null;
  const list = cache[mode][key];
  return Array.isArray(list) && list.length > 0 ? list : null;
}

export function writeCachedAIModels(
  cache: AIModelsCache,
  mode: AIModelCacheMode,
  rawKey: string,
  models: string[],
  storage?: StorageLike,
): AIModelsCache {
  const key = normalizeModelCacheKey(mode, rawKey);
  const clean = sanitizeModels(models);
  if (!key || clean.length === 0) return cache;
  const next: AIModelsCache = {
    api: mode === 'api' ? { ...cache.api, [key]: clean } : cache.api,
    cli: mode === 'cli' ? { ...cache.cli, [key]: clean } : cache.cli,
  };
  saveAIModelsCache(next, storage);
  return next;
}
