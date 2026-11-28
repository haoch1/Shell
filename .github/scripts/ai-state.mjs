import assert from 'node:assert/strict';

export async function verifyAIState({ call, evaluate, until, click }) {
  const page = (fn, ...args) => evaluate('(' + fn.toString() + ')(...' + JSON.stringify(args) + ')');
  const wait = (label, fn, ...args) => until(() => page(fn, ...args), label);
  const fill = (selector, value) => page((selector, value) => {
    const el = document.querySelector(selector);
    const prototype = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(el, value);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }, selector, value);

  await page(() => { window.__beforeAIStateReload = true; });
  await call('Page.reload');
  await wait('fresh AI fixture workspace', () => !window.__beforeAIStateReload &&
    !document.querySelector('[role=tab]') && document.querySelectorAll('.host-row').length === 3);
  await page(() => {
    const fixture = window.aiStateFixture = { modelRequests: [], turns: [], detections: 0, configReads: 0 };
    const api = window.go.main.App;
    const read = api.AIConfig;
    api.AIConfig = async () => { fixture.configReads++; return { ...(await read()), hasKey: true }; };
    api.DetectAICLIs = async () => { fixture.detections++; return []; };
    api.FetchAIModels = config => new Promise(resolve => {
      fixture.modelRequests.push({ blank: config.apiKey === '', keep: config.keepKey, resolve });
    });
    api.RunAIAgent = async (id, goal, history) => {
      fixture.turns.push({ id, goal, history });
      if (fixture.turns.length === 1) {
        return new Promise((_resolve, reject) => { fixture.finishFirst = () => reject(new Error('first fixture failed')); });
      }
      return 'fixture reply';
    };
  });

  await click('#settingsBtn');
  await wait('settings without AI discovery', () => !!document.querySelector('#settingsDialog'));
  assert.deepEqual(await page(() => [window.aiStateFixture.configReads, window.aiStateFixture.detections]), [0, 0]);
  await click('.settings-nav-btn:nth-child(4)');
  await wait('fixture AI settings', () => !!document.querySelector('#endpoint'));
  assert.equal(await page(() => window.aiStateFixture.detections), 0);
  await fill('#apiKey', 'fixture-key');
  await fill('#endpoint', 'https://first.example.invalid/v1');
  await wait('clear the previous provider key', () => document.querySelector('#apiKey').value === '');
  await click('#fetchModelsBtn');
  await wait('first model refresh', () => window.aiStateFixture.modelRequests.length === 1);
  await fill('#endpoint', 'https://second.example.invalid/v1');
  await wait('new provider can start its own refresh', () => !document.querySelector('#fetchModelsBtn').disabled);
  await click('#fetchModelsBtn');
  await wait('second model refresh', () => window.aiStateFixture.modelRequests.length === 2);
  assert.deepEqual(await page(() => window.aiStateFixture.modelRequests.map(r => [r.blank, r.keep])),
    [[true, false], [true, false]], 'a different provider must receive neither the typed key nor the saved key');
  await page(() => window.aiStateFixture.modelRequests[0].resolve(['stale-model']));
  assert.deepEqual(await page(() => [document.querySelector('#fetchModelsBtn').disabled,
    document.querySelector('#model').value, !!document.querySelector('#aiModelMenu')]), [true, '', false]);
  await fill('#model', 'typed-while-loading');
  await page(() => window.aiStateFixture.modelRequests[1].resolve(['fresh-model']));
  await wait('second model refresh completed', () => !document.querySelector('#fetchModelsBtn').disabled);
  assert.equal(await page(() => document.querySelector('#model').value), 'typed-while-loading');
  const cached = await page(() => JSON.parse(localStorage.getItem('shell:ai-models-cache:v1')).api);
  assert.equal(cached['https://first.example.invalid/v1'], undefined);
  assert.deepEqual(cached['https://second.example.invalid/v1'], ['fresh-model']);
  await click('#settingsDialog button[aria-label="关闭设置"]');

  await click('.host-row[data-profile-id="p1"]');
  await wait('first fixture session', () => document.querySelector('.xterm-screen') && !document.querySelector('#sftpBtn').disabled);
  await click('#aiOpen');
  await wait('ready AI on first session', () => document.querySelector('.ai-target-text')?.textContent.includes('Tokyo Production') &&
    document.querySelector('.ai-quick-chip')?.disabled === false);
  await fill('#aiPromptInput', 'first private fixture question');
  await click('#aiSubmitBtn');
  await wait('pending first fixture question', () => !!window.aiStateFixture.finishFirst);
  await fill('#aiPromptInput', 'unsent first draft');
  await click('.host-row[data-profile-id="p2"]');
  await wait('AI follows second fixture session', () => document.querySelector('.ai-target-text')?.textContent.includes('SG - AWS'));
  assert.deepEqual(await page(() => [document.querySelector('#aiPromptInput').value,
    document.querySelectorAll('.ai-msg-user,.ai-msg-assistant').length,
    document.querySelector('#aiSubmitBtn').disabled,
    document.querySelector('#aiSubmitBtn').getAttribute('aria-label')]), ['', 0, true, '发送并执行']);
  await page(() => window.aiStateFixture.finishFirst());
  await fill('#aiPromptInput', 'second independent fixture question');
  await wait('second fixture question enabled', () => !document.querySelector('#aiSubmitBtn').disabled);
  await click('#aiSubmitBtn');
  await wait('reply to second fixture question', () => document.querySelector('.ai-md')?.textContent === 'fixture reply');
  const turns = await page(() => window.aiStateFixture.turns);
  assert.equal(turns.length, 2);
  assert.notEqual(turns[0].id, turns[1].id);
  assert.ok(turns.every(turn => turn.id && turn.history.length === 0));
  assert.equal(await page(() => !!document.querySelector('.ai-error-row')), false);
  await click('.host-row[data-profile-id="p1"]');
  await wait('first session restores draft, error and retry', () => document.querySelector('#aiPromptInput')?.value === 'unsent first draft' &&
    document.querySelector('.ai-error-row')?.textContent.includes('first fixture failed') &&
    document.querySelector('.ai-msg-user-bubble')?.textContent === 'first private fixture question');
}
