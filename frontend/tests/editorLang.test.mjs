import assert from 'node:assert/strict';
import test from 'node:test';
import { EditorState } from '@codemirror/state';
import { foldable, ensureSyntaxTree } from '@codemirror/language';
import {
  EDITOR_LANGUAGES,
  applyLineEnding,
  detectLanguageId,
  detectLineEnding,
  indentFoldService,
  loadLanguageExtension,
  normalizeToLF,
} from '../src/lib/editorLang.ts';

test('EDITOR_LANGUAGES includes every required language with unique ids and readable labels', () => {
  const expectedIds = [
    'plain',
    'shell',
    'nginx',
    'yaml',
    'json',
    'toml',
    'ini',
    'dockerfile',
    'javascript',
    'typescript',
    'python',
    'go',
    'sql',
    'html',
    'css',
    'xml',
    'markdown',
    'diff',
  ];

  const actualIds = EDITOR_LANGUAGES.map((l) => l.id);
  assert.deepEqual(new Set(actualIds), new Set(expectedIds));
  assert.equal(actualIds.length, expectedIds.length);

  for (const lang of EDITOR_LANGUAGES) {
    assert.ok(lang.label.trim().length > 0, `expected non-empty label for ${lang.id}`);
  }
});

test('detectLanguageId matches DevOps & Unix special basenames', () => {
  const cases = [
    ['/app/Dockerfile', 'dockerfile'],
    ['/app/Dockerfile.prod', 'dockerfile'],
    ['/app/dockerfile.dev', 'dockerfile'],
    ['/app/build.dockerfile', 'dockerfile'],
    ['/srv/project/Makefile', 'shell'],
    ['/srv/project/GNUmakefile', 'shell'],
    ['/etc/nginx/nginx.conf', 'nginx'],
    ['nginx.conf', 'nginx'],
    ['/etc/nginx/conf.d/default.conf', 'nginx'],
    ['/etc/nginx/sites-available/api', 'nginx'],
    ['/etc/nginx/sites-enabled/default', 'nginx'],
    ['/root/.bashrc', 'shell'],
    ['/home/ops/.zshrc', 'shell'],
    ['/home/ops/.profile', 'shell'],
    ['/etc/crontab', 'shell'],
    ['/srv/app/.env', 'ini'],
    ['/srv/app/.env.local', 'ini'],
    ['/srv/app/.env.production', 'ini'],
    ['/etc/hosts', 'ini'],
    ['/etc/fstab', 'ini'],
    ['/etc/ssh/sshd_config', 'ini'],
    ['/root/.ssh/ssh_config', 'ini'],
    ['/srv/docker-compose.yml', 'yaml'],
    ['/srv/docker-compose.prod.yaml', 'yaml'],
    ['/etc/systemd/system/app.service', 'ini'],
    ['/etc/systemd/system/backup.timer', 'ini'],
    ['/etc/systemd/system/docker.socket', 'ini'],
    ['/etc/redis/redis.conf', 'ini'],
    ['/etc/haproxy/haproxy.cfg', 'ini'],
    ['/etc/mysql/my.cnf', 'ini'],
  ];

  for (const [remotePath, expected] of cases) {
    assert.equal(detectLanguageId(remotePath), expected, `path=${remotePath}`);
  }
});

test('detectLanguageId matches standard code, markup, data, and diff extensions', () => {
  const cases = [
    ['/usr/local/bin/deploy.sh', 'shell'],
    ['/opt/init.bash', 'shell'],
    ['/home/me/aliases.zsh', 'shell'],
    ['/app/package.json', 'json'],
    ['/app/tsconfig.jsonc', 'json'],
    ['/etc/prometheus/prometheus.yml', 'yaml'],
    ['/k8s/deployment.yaml', 'yaml'],
    ['/srv/Cargo.toml', 'toml'],
    ['/etc/php/php.ini', 'ini'],
    ['/srv/app/config.env', 'ini'],
    ['/srv/app/application.properties', 'ini'],
    ['/app/index.js', 'javascript'],
    ['/app/server.mjs', 'javascript'],
    ['/app/legacy.cjs', 'javascript'],
    ['/app/Widget.jsx', 'javascript'],
    ['/app/main.ts', 'typescript'],
    ['/app/App.tsx', 'typescript'],
    ['/srv/worker.py', 'python'],
    ['/srv/cmd/server/main.go', 'go'],
    ['/srv/migrations/001_init.sql', 'sql'],
    ['/var/www/index.html', 'html'],
    ['/var/www/error.htm', 'html'],
    ['/var/www/app.css', 'css'],
    ['/var/www/theme.scss', 'css'],
    ['/srv/pom.xml', 'xml'],
    ['/var/www/logo.svg', 'xml'],
    ['/srv/README.md', 'markdown'],
    ['/srv/CHANGELOG.markdown', 'markdown'],
    ['/tmp/fix.diff', 'diff'],
    ['/tmp/hotfix.patch', 'diff'],
  ];

  for (const [remotePath, expected] of cases) {
    assert.equal(detectLanguageId(remotePath), expected, `path=${remotePath}`);
  }
});

test('detectLanguageId handles uppercase extensions, Windows paths, and plain-text fallbacks', () => {
  assert.equal(detectLanguageId('C:\\srv\\CONFIG.JSON'), 'json');
  assert.equal(detectLanguageId('/var/log/syslog.YAML'), 'yaml');
  assert.equal(detectLanguageId('/var/log/nginx/access.log'), 'plain');
  assert.equal(detectLanguageId('/srv/notes.txt'), 'plain');
  assert.equal(detectLanguageId('/srv/README'), 'plain');
  assert.equal(detectLanguageId('/srv/.unknownrc'), 'plain');
  assert.equal(detectLanguageId(''), 'plain');
  assert.equal(detectLanguageId('/'), 'plain');
  assert.equal(detectLanguageId('   /etc/hosts   '), 'ini');
});

test('loadLanguageExtension returns a valid CodeMirror 6 Extension for every supported language', () => {
  for (const { id } of EDITOR_LANGUAGES) {
    const ext = loadLanguageExtension(id);
    assert.ok(ext !== undefined && ext !== null, `expected extension for ${id}`);
    const state = EditorState.create({
      doc: 'hello world\nsecond line\n',
      extensions: [ext],
    });
    assert.equal(state.doc.lines, 3, `state creation failed for ${id}`);
  }
});

test('StreamLanguage modes produce syntax trees and indentFoldService folds YAML/Nginx blocks', () => {
  const yamlDoc = [
    '# Stack',
    'services:',
    '  api:',
    '    image: nginx:alpine',
    '    ports:',
    '      - "443:443"',
    'volumes:',
    '  data: {}',
  ].join('\n');
  const yamlState = EditorState.create({
    doc: yamlDoc,
    extensions: [loadLanguageExtension('yaml'), indentFoldService],
  });
  const yamlTree = ensureSyntaxTree(yamlState, yamlDoc.length, 500);
  assert.ok(yamlTree && yamlTree.length === yamlDoc.length, 'expected parsed YAML syntax tree');

  const servicesLine = yamlState.doc.line(2);
  const fold = foldable(yamlState, servicesLine.from, servicesLine.to);
  assert.deepEqual(fold, { from: servicesLine.to, to: yamlState.doc.line(6).to });

  const nginxDoc = 'server {\n    listen 443 ssl;\n    location / {\n        proxy_pass http://app;\n    }\n}\n';
  const nginxState = EditorState.create({
    doc: nginxDoc,
    extensions: [loadLanguageExtension('nginx'), indentFoldService],
  });
  const serverLine = nginxState.doc.line(1);
  const nginxFold = foldable(nginxState, serverLine.from, serverLine.to);
  assert.deepEqual(nginxFold, { from: serverLine.to, to: nginxState.doc.line(5).to });

  const mdDoc = '# Title\n\n- **bold** and *italic* with `code`\n```sh\necho hi\n```\n';
  const mdState = EditorState.create({
    doc: mdDoc,
    extensions: [loadLanguageExtension('markdown'), indentFoldService],
  });
  const mdTree = ensureSyntaxTree(mdState, mdDoc.length, 500);
  assert.ok(mdTree && mdTree.length === mdDoc.length, 'expected parsed Markdown syntax tree');
});

test('line ending helpers detect, normalize, and convert LF/CRLF cleanly', () => {
  assert.equal(detectLineEnding(''), 'LF');
  assert.equal(detectLineEnding('a\nb\nc'), 'LF');
  assert.equal(detectLineEnding('a\r\nb\r\n'), 'CRLF');

  assert.equal(normalizeToLF('a\r\nb\nc\r\n'), 'a\nb\nc\n');
  assert.equal(applyLineEnding('a\nb\nc', 'CRLF'), 'a\r\nb\r\nc');
  assert.equal(applyLineEnding('a\r\nb\r\nc', 'LF'), 'a\nb\nc');
  assert.equal(applyLineEnding('a\r\nb\nc', 'CRLF'), 'a\r\nb\r\nc');
});
