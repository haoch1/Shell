import test from 'node:test';
import assert from 'node:assert/strict';
import {
  createWindowFocusGuard,
  KEYBOARD_NAV_WINDOW_MS,
  shouldBlurChromeButtonOnDeactivate,
  WINDOW_RESTORE_SUPPRESS_MS,
} from '../src/lib/windowFocusGuard.ts';

test('tooltip does not open on focus after window minimize/restore or Alt+Tab', () => {
  let clock = 1000;
  const guard = createWindowFocusGuard(() => clock);

  let dismissed = 0;
  const off = guard.subscribeWindowDismiss(() => {
    dismissed += 1;
  });

  // Programmatic or mouse focus without keyboard navigation must not open tooltips
  assert.equal(guard.shouldOpenTooltipOnFocus(false), false);
  assert.equal(guard.shouldOpenTooltipOnFocus(true), false);

  // Minimize / hide / Alt+Tab away triggers dismiss listeners and starts focus suppression
  guard.noteWindowInactive();
  assert.equal(dismissed, 1);
  assert.equal(guard.isWindowRestoringFocus(), true);
  assert.equal(guard.shouldOpenTooltipOnFocus(true), false);
  assert.equal(guard.shouldOpenDropdownOnFocus(), false);

  // Reopen / restore window: immediate focus restoration on first button (#toggleSidebar) is suppressed
  clock += 2000;
  guard.noteWindowRestored();
  assert.equal(guard.isWindowRestoringFocus(), true);
  assert.equal(guard.shouldOpenTooltipOnFocus(true), false);
  assert.equal(guard.shouldOpenTooltipOnFocus(false), false);
  assert.equal(guard.shouldOpenDropdownOnFocus(), false);

  // After restore grace window passes, dropdown focus works again, while non-keyboard tooltip focus stays blocked
  clock += WINDOW_RESTORE_SUPPRESS_MS + 10;
  assert.equal(guard.isWindowRestoringFocus(), false);
  assert.equal(guard.shouldOpenDropdownOnFocus(), true);
  assert.equal(guard.shouldOpenTooltipOnFocus(true), false);

  off();
});

test('tooltip opens on genuine keyboard Tab focus and clears after pointer input or expiry', () => {
  let clock = 5000;
  const guard = createWindowFocusGuard(() => clock);

  guard.noteKeyboardNav();
  assert.equal(guard.shouldOpenTooltipOnFocus(true), true);
  assert.equal(guard.shouldOpenTooltipOnFocus(false), false);

  // Pointer click clears keyboard navigation intent immediately
  guard.notePointerInput();
  assert.equal(guard.shouldOpenTooltipOnFocus(true), false);

  // Keyboard navigation intent expires after KEYBOARD_NAV_WINDOW_MS
  guard.noteKeyboardNav();
  clock += KEYBOARD_NAV_WINDOW_MS + 1;
  assert.equal(guard.shouldOpenTooltipOnFocus(true), false);

  // Keyboard navigation while inactive is ignored
  guard.noteWindowInactive();
  guard.noteKeyboardNav();
  assert.equal(guard.shouldOpenTooltipOnFocus(true), false);
});

test('shouldBlurChromeButtonOnDeactivate targets toolbar/tooltip buttons only and preserves editable controls', () => {
  const mkEl = (tagName, attrs = {}, matchSelector = null) => ({
    tagName,
    hasAttribute: (name) => Object.prototype.hasOwnProperty.call(attrs, name),
    closest: (sel) => (matchSelector && sel.includes(matchSelector) ? {} : null),
  });

  assert.equal(shouldBlurChromeButtonOnDeactivate(null), false);
  assert.equal(shouldBlurChromeButtonOnDeactivate(mkEl('INPUT', {}, '.titlebar')), false);
  assert.equal(shouldBlurChromeButtonOnDeactivate(mkEl('TEXTAREA', {}, '.titlebar')), false);
  assert.equal(shouldBlurChromeButtonOnDeactivate(mkEl('BUTTON', {}, null)), false);
  assert.equal(shouldBlurChromeButtonOnDeactivate(mkEl('BUTTON', { 'data-tooltip-trigger': '' })), true);
  assert.equal(shouldBlurChromeButtonOnDeactivate(mkEl('BUTTON', {}, '.titlebar')), true);
  assert.equal(shouldBlurChromeButtonOnDeactivate(mkEl('BUTTON', {}, '.sidebar-top')), true);
  assert.equal(shouldBlurChromeButtonOnDeactivate(mkEl('BUTTON', {}, '.sidebar-footer')), true);
});
