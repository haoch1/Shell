// Coordinates transient focus-triggered UI (Radix Tooltips, ComboBox/model dropdowns,
// context menus, and popovers) across window minimize/restore, tray hide/show, and Alt+Tab.

const KEYBOARD_NAV_KEYS = new Set([
  'Tab',
  'ArrowUp',
  'ArrowDown',
  'ArrowLeft',
  'ArrowRight',
  'Home',
  'End',
  'PageUp',
  'PageDown',
]);

export const KEYBOARD_NAV_WINDOW_MS = 300;
export const WINDOW_RESTORE_SUPPRESS_MS = 450;

export interface WindowFocusGuard {
  noteKeyboardNav(): void;
  notePointerInput(): void;
  noteWindowInactive(): void;
  noteWindowRestored(): void;
  isWindowInactive(): boolean;
  isWindowRestoringFocus(): boolean;
  shouldOpenTooltipOnFocus(isFocusVisible: boolean): boolean;
  shouldOpenDropdownOnFocus(): boolean;
  subscribeWindowDismiss(listener: () => void): () => void;
}

export function createWindowFocusGuard(now: () => number = () => Date.now()): WindowFocusGuard {
  let lastKeyNavAt = -Infinity;
  let windowInactive = false;
  let suppressFocusUntil = -Infinity;
  const dismissListeners = new Set<() => void>();

  const emitDismiss = () => {
    for (const listener of [...dismissListeners]) {
      try {
        listener();
      } catch {
        // ignore listener errors
      }
    }
  };

  const isWindowRestoringFocus = (): boolean => windowInactive || now() < suppressFocusUntil;

  return {
    noteKeyboardNav() {
      if (windowInactive) return;
      lastKeyNavAt = now();
    },
    notePointerInput() {
      lastKeyNavAt = -Infinity;
    },
    noteWindowInactive() {
      windowInactive = true;
      suppressFocusUntil = now() + WINDOW_RESTORE_SUPPRESS_MS;
      lastKeyNavAt = -Infinity;
      emitDismiss();
    },
    noteWindowRestored() {
      windowInactive = false;
      suppressFocusUntil = now() + WINDOW_RESTORE_SUPPRESS_MS;
      lastKeyNavAt = -Infinity;
    },
    isWindowInactive(): boolean {
      return windowInactive;
    },
    isWindowRestoringFocus,
    shouldOpenTooltipOnFocus(isFocusVisible: boolean): boolean {
      if (!isFocusVisible || isWindowRestoringFocus()) return false;
      return now() - lastKeyNavAt <= KEYBOARD_NAV_WINDOW_MS;
    },
    shouldOpenDropdownOnFocus(): boolean {
      return !isWindowRestoringFocus();
    },
    subscribeWindowDismiss(listener: () => void): () => void {
      dismissListeners.add(listener);
      return () => {
        dismissListeners.delete(listener);
      };
    },
  };
}

export function shouldBlurChromeButtonOnDeactivate(el: Element | null): boolean {
  if (!el || el.tagName !== 'BUTTON') return false;
  return (
    el.hasAttribute('data-tooltip-trigger') ||
    Boolean(el.closest?.('.titlebar, .sidebar-top, .sidebar-footer'))
  );
}

let singleton: WindowFocusGuard | null = null;
let installed = false;

function blurStaleChromeButton() {
  if (typeof document === 'undefined') return;
  const active = document.activeElement;
  if (shouldBlurChromeButtonOnDeactivate(active) && active instanceof HTMLElement) {
    active.blur();
  }
}

export function ensureWindowFocusGuardInstalled(): WindowFocusGuard {
  if (!singleton) {
    singleton = createWindowFocusGuard();
  }
  if (!installed && typeof window !== 'undefined' && typeof document !== 'undefined') {
    installed = true;
    const guard = singleton;

    window.addEventListener(
      'keydown',
      (e: KeyboardEvent) => {
        if (!e.ctrlKey && !e.metaKey && !e.altKey && KEYBOARD_NAV_KEYS.has(e.key)) {
          guard.noteKeyboardNav();
        }
      },
      true,
    );

    window.addEventListener(
      'pointerdown',
      () => {
        guard.notePointerInput();
      },
      true,
    );

    window.addEventListener('blur', () => {
      blurStaleChromeButton();
      guard.noteWindowInactive();
    });

    window.addEventListener('focus', () => {
      guard.noteWindowRestored();
    });

    document.addEventListener('visibilitychange', () => {
      if (document.hidden) {
        blurStaleChromeButton();
        guard.noteWindowInactive();
      } else {
        guard.noteWindowRestored();
      }
    });
  }
  return singleton;
}

export function isWindowInactive(): boolean {
  return ensureWindowFocusGuardInstalled().isWindowInactive();
}

export function isWindowRestoringFocus(): boolean {
  return ensureWindowFocusGuardInstalled().isWindowRestoringFocus();
}

export function shouldOpenTooltipOnFocus(isFocusVisible: boolean): boolean {
  return ensureWindowFocusGuardInstalled().shouldOpenTooltipOnFocus(isFocusVisible);
}

export function shouldOpenDropdownOnFocus(): boolean {
  return ensureWindowFocusGuardInstalled().shouldOpenDropdownOnFocus();
}

export function subscribeWindowDismiss(listener: () => void): () => void {
  return ensureWindowFocusGuardInstalled().subscribeWindowDismiss(listener);
}
