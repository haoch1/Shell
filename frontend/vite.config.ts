import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';

const frontendDir = path.dirname(fileURLToPath(import.meta.url));

/**
 * Rewrites barrel imports from `lucide-react` to direct per-icon ESM modules
 * so Vite/Rollup avoids parsing all ~1,640 unused icons on every build.
 */
function lucideDirectImport(): Plugin {
  let iconMap: Map<string, string> | null = null;

  function loadIconMap(): Map<string, string> {
    if (iconMap) return iconMap;
    iconMap = new Map();
    try {
      const barrel = readFileSync(
        path.join(frontendDir, 'node_modules/lucide-react/dist/esm/lucide-react.js'),
        'utf8',
      );
      const exportRe = /export\s*\{([^}]+)\}\s*from\s*'\.\/icons\/([^']+\.js)'/g;
      for (const match of barrel.matchAll(exportRe)) {
        const target = `lucide-react/dist/esm/icons/${match[2]}`;
        for (const part of match[1].split(',')) {
          const alias = part.trim().match(/^default\s+as\s+([A-Za-z0-9_$]+)$/);
          if (alias) iconMap.set(alias[1], target);
        }
      }
    } catch {
      // Fallback safely to the standard barrel if package layout changes.
    }
    return iconMap;
  }

  return {
    name: 'lucide-direct-import',
    transform(code, id) {
      if (!code.includes('lucide-react') || id.includes('node_modules')) return null;
      const map = loadIconMap();
      if (map.size === 0) return null;

      let changed = false;
      const rewritten = code.replace(
        /import\s*\{([^}]+)\}\s*from\s*['"]lucide-react['"]\s*;?/g,
        (full, specList: string) => {
          const direct: string[] = [];
          const fallback: string[] = [];
          for (const rawSpec of specList.split(',')) {
            const spec = rawSpec.trim();
            if (!spec) continue;
            const m = spec.match(/^([A-Za-z0-9_$]+)(?:\s+as\s+([A-Za-z0-9_$]+))?$/);
            const target = m ? map.get(m[1]) : undefined;
            if (m && target) {
              direct.push(`import ${m[2] ?? m[1]} from '${target}';`);
            } else {
              fallback.push(spec);
            }
          }
          if (direct.length === 0) return full;
          changed = true;
          if (fallback.length > 0) {
            direct.push(`import { ${fallback.join(', ')} } from 'lucide-react';`);
          }
          return direct.join('\n');
        },
      );
      return changed ? { code: rewritten, map: null } : null;
    },
  };
}

function resolveManualChunk(id: string): string | undefined {
  const normalized = id.replace(/\\/g, '/');
  if (normalized.endsWith('/src/lib/editorLang.ts')) return 'codemirror';
  if (!normalized.includes('/node_modules/')) return undefined;
  if (normalized.includes('/@xterm/addon-webgl/')) return 'xterm-webgl';
  if (normalized.includes('/@xterm/')) return 'xterm';
  if (
    normalized.includes('/@codemirror/') ||
    normalized.includes('/@lezer/') ||
    normalized.includes('/style-mod/') ||
    normalized.includes('/w3c-keyname/') ||
    normalized.includes('/crelt/') ||
    normalized.includes('/@marijn/')
  ) {
    return 'codemirror';
  }
  if (
    normalized.includes('/react/') ||
    normalized.includes('/react-dom/') ||
    normalized.includes('/scheduler/') ||
    normalized.includes('/@radix-ui/') ||
    normalized.includes('/@floating-ui/') ||
    normalized.includes('/react-remove-scroll') ||
    normalized.includes('/use-callback-ref/') ||
    normalized.includes('/use-sidecar/') ||
    normalized.includes('/aria-hidden/') ||
    normalized.includes('/detect-node-es/') ||
    normalized.includes('/get-nonce/') ||
    normalized.includes('/tslib/') ||
    normalized.includes('/lucide-react/') ||
    normalized.includes('/@tanstack/')
  ) {
    return 'vendor';
  }
  return undefined;
}

export default defineConfig({
  plugins: [react(), lucideDirectImport()],
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    target: 'es2022',
    rollupOptions: {
      output: {
        manualChunks: resolveManualChunk,
      },
    },
  },
});
