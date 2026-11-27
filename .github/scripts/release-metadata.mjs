import assert from 'node:assert/strict';
import { appendFile, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';

const read = file => readFile(new URL('../../' + file, import.meta.url), 'utf8');
const { version } = JSON.parse(await read('frontend/package.json'));
const lock = JSON.parse(await read('frontend/package-lock.json'));
assert.match(version, /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/);
assert.equal(lock.version, version, 'package-lock.json version differs from package.json');
assert.equal(lock.packages[''].version, version, 'package-lock.json root package version differs from package.json');

const refTag = process.env.GITHUB_REF_TYPE === 'tag' ? process.env.GITHUB_REF_NAME : '';
const inputTag = process.env.RELEASE_TAG || '';
for (const tag of [refTag, inputTag].filter(Boolean)) {
  assert.equal(tag, 'v' + version, 'Release tag must match frontend/package.json');
}

const changelog = await read('CHANGELOG.md');
const headings = [...changelog.matchAll(/^## \[([^\]]+)\][^\r\n]*$/gm)];
const index = headings.findIndex(match => match[1] === version);
assert.notEqual(index, -1, 'Missing CHANGELOG section for version ' + version);
assert.equal(index, 0, 'The first versioned CHANGELOG section must match frontend/package.json');
const heading = headings[index];
const notes = changelog.slice(heading.index + heading[0].length, headings[index + 1]?.index).trim();
assert.ok(notes, 'Empty CHANGELOG section for version ' + version);

if (process.env.GITHUB_OUTPUT) {
  await writeFile(path.join(process.env.RUNNER_TEMP, 'release-notes.md'), notes + '\n');
  await appendFile(process.env.GITHUB_OUTPUT, 'tag=' + (refTag || inputTag) + '\nversion=' + version + '\n');
}
console.log('Verified Shell ' + version + ', package lock and release notes.');
