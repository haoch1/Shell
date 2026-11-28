import test from 'node:test';
import assert from 'node:assert/strict';
import {
  AI_MODELS_CACHE_KEY,
  createEmptyAIModelsCache,
  getCachedAIModels,
  getCachedSelectedAIModel,
  loadAIModelsCache,
  normalizeCliCommand,
  normalizeEndpoint,
  writeCachedAIModels,
  writeCachedSelectedAIModel,
} from '../src/lib/aiModelCache.ts';

function createMemoryStorage(initial = {}) {
  const map = new Map(Object.entries(initial));
  return {
    getItem(key) {
      return map.has(key) ? map.get(key) : null;
    },
    setItem(key, value) {
      map.set(key, String(value));
    },
    dump() {
      return Object.fromEntries(map.entries());
    },
  };
}

test('aiModelCache persists API endpoints and CLI commands independently across reloads', () => {
  const storage = createMemoryStorage();

  let cache = loadAIModelsCache(storage);
  assert.deepEqual(cache, createEmptyAIModelsCache());

  cache = writeCachedAIModels(
    cache,
    'api',
    ' https://api.anthropic.com/v1/// ',
    ['claude-sonnet-5-5', 'claude-opus-5-5', 'claude-sonnet-5-5', '  '],
    storage,
  );
  cache = writeCachedAIModels(
    cache,
    'api',
    'https://api.openai.com/v1/',
    ['gpt-6.2-sol', 'gpt-6.2-astra'],
    storage,
  );
  cache = writeCachedAIModels(
    cache,
    'cli',
    '  AGY  ',
    ['gemini-4-neon-high', 'claude-sonnet-5-5-high'],
    storage,
  );
  cache = writeCachedAIModels(
    cache,
    'cli',
    'codex',
    ['gpt-6.2-sol', 'gpt-6.1-terra'],
    storage,
  );

  // Reload from storage to simulate closing/reopening SettingsModal or restarting Shell.exe
  const reloaded = loadAIModelsCache(storage);
  assert.deepEqual(getCachedAIModels(reloaded, 'api', 'https://api.anthropic.com/v1'), [
    'claude-sonnet-5-5',
    'claude-opus-5-5',
  ]);
  assert.deepEqual(getCachedAIModels(reloaded, 'api', 'https://api.openai.com/v1///'), [
    'gpt-6.2-sol',
    'gpt-6.2-astra',
  ]);
  assert.deepEqual(getCachedAIModels(reloaded, 'cli', 'agy'), [
    'gemini-4-neon-high',
    'claude-sonnet-5-5-high',
  ]);
  assert.deepEqual(getCachedAIModels(reloaded, 'cli', 'CODEX '), [
    'gpt-6.2-sol',
    'gpt-6.1-terra',
  ]);
  assert.equal(getCachedAIModels(reloaded, 'cli', 'claude'), null);

  // Refreshing one key overwrites only that key without clobbering others
  const updated = writeCachedAIModels(reloaded, 'cli', 'codex', ['gpt-6.3-preview'], storage);
  const reloadedAgain = loadAIModelsCache(storage);
  assert.deepEqual(getCachedAIModels(updated, 'cli', 'codex'), ['gpt-6.3-preview']);
  assert.deepEqual(getCachedAIModels(reloadedAgain, 'cli', 'codex'), ['gpt-6.3-preview']);
  assert.deepEqual(getCachedAIModels(reloadedAgain, 'cli', 'agy'), [
    'gemini-4-neon-high',
    'claude-sonnet-5-5-high',
  ]);
  assert.deepEqual(getCachedAIModels(reloadedAgain, 'api', 'https://api.anthropic.com/v1'), [
    'claude-sonnet-5-5',
    'claude-opus-5-5',
  ]);
});

test('aiModelCache ignores empty writes and tolerates malformed or corrupted JSON', () => {
  const corrupt = createMemoryStorage({ [AI_MODELS_CACHE_KEY]: '{not-valid-json' });
  assert.deepEqual(loadAIModelsCache(corrupt), createEmptyAIModelsCache());

  const wrongShape = createMemoryStorage({ [AI_MODELS_CACHE_KEY]: '["not-an-object"]' });
  assert.deepEqual(loadAIModelsCache(wrongShape), createEmptyAIModelsCache());

  const noisy = createMemoryStorage({
    [AI_MODELS_CACHE_KEY]: JSON.stringify({
      api: {
        'https://api.example.com/v1/': ['m1', 42, '', 'm1', ' m2 '],
        '   ': ['ignored'],
        bad: 'not-array',
      },
      cli: {
        ' Codex ': ['c1', null, 'c2'],
        empty: [],
      },
    }),
  });
  const cleaned = loadAIModelsCache(noisy);
  assert.deepEqual(cleaned, {
    api: { 'https://api.example.com/v1': ['m1', 'm2'] },
    cli: { codex: ['c1', 'c2'] },
    selectedApi: {},
    selectedCli: {},
  });

  // Empty key or empty model list must not clobber existing cache
  const unchanged1 = writeCachedAIModels(cleaned, 'cli', 'codex', [], noisy);
  assert.equal(unchanged1, cleaned);
  const unchanged2 = writeCachedAIModels(cleaned, 'api', '   ', ['x'], noisy);
  assert.equal(unchanged2, cleaned);

  assert.equal(normalizeEndpoint(' https://host/v1/// '), 'https://host/v1');
  assert.equal(normalizeCliCommand('  AgY  '), 'agy');
});

test('aiModelCache handles throwing storage gracefully without crashing', () => {
  const brokenStorage = {
    getItem() {
      throw new Error('storage blocked');
    },
    setItem() {
      throw new Error('quota exceeded');
    },
  };
  const cache = loadAIModelsCache(brokenStorage);
  assert.deepEqual(cache, createEmptyAIModelsCache());
  const next = writeCachedAIModels(cache, 'cli', 'agy', ['gemini-4-neon-high'], brokenStorage);
  assert.deepEqual(getCachedAIModels(next, 'cli', 'agy'), ['gemini-4-neon-high']);
});

test('aiModelCache remembers selected models per CLI tool and API endpoint independently', () => {
  const storage = createMemoryStorage();
  let cache = loadAIModelsCache(storage);

  // Select a model under Antigravity CLI (agy) — Codex CLI must stay empty (never inherit agy's model)
  cache = writeCachedSelectedAIModel(cache, 'cli', 'AGY', 'gemini-4-neon-medium', storage);
  assert.equal(getCachedSelectedAIModel(cache, 'cli', 'agy'), 'gemini-4-neon-medium');
  assert.equal(getCachedSelectedAIModel(cache, 'cli', 'codex'), '');
  assert.equal(getCachedSelectedAIModel(cache, 'cli', 'claude'), '');

  // Select a separate model under Codex CLI and an API endpoint
  cache = writeCachedSelectedAIModel(cache, 'cli', 'codex', 'gpt-6.2-sol', storage);
  cache = writeCachedSelectedAIModel(cache, 'api', 'https://api.anthropic.com/v1/', 'claude-opus-5-5', storage);

  // Refreshing model lists must preserve remembered per-tool selections
  cache = writeCachedAIModels(cache, 'cli', 'codex', ['gpt-6.2-sol', 'gpt-6.1-terra'], storage);

  const reloaded = loadAIModelsCache(storage);
  assert.equal(getCachedSelectedAIModel(reloaded, 'cli', 'agy'), 'gemini-4-neon-medium');
  assert.equal(getCachedSelectedAIModel(reloaded, 'cli', 'codex'), 'gpt-6.2-sol');
  assert.equal(getCachedSelectedAIModel(reloaded, 'api', 'https://api.anthropic.com/v1'), 'claude-opus-5-5');
  assert.equal(getCachedSelectedAIModel(reloaded, 'api', 'https://api.openai.com/v1'), '');

  // Clearing Codex back to default ('') must remove only Codex without affecting agy
  const cleared = writeCachedSelectedAIModel(reloaded, 'cli', 'codex', '   ', storage);
  const reloadedAfterClear = loadAIModelsCache(storage);
  assert.equal(getCachedSelectedAIModel(cleared, 'cli', 'codex'), '');
  assert.equal(getCachedSelectedAIModel(reloadedAfterClear, 'cli', 'codex'), '');
  assert.equal(getCachedSelectedAIModel(reloadedAfterClear, 'cli', 'agy'), 'gemini-4-neon-medium');
});
