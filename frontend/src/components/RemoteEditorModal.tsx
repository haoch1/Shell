import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  FileCode2,
  Loader2,
  Maximize2,
  Minimize2,
  RefreshCw,
  Save,
  Search,
  WrapText,
  X,
} from 'lucide-react';
import type { Compartment, EditorState, Extension } from '@codemirror/state';
import type { EditorView } from '@codemirror/view';
import { errorMessage, getApi } from '../lib/bridge';
import type { EditorLanguageId, EditorLanguageOption, LineEnding } from '../lib/editorLang';
import { formatBytes } from '../lib/utils';
import { Select } from './ui/Select';
import '../styles/editor.css';

interface RemoteEditorModalProps {
  sessionId: string;
  remotePath: string;
  isDark?: boolean;
  onClose: () => void;
  onSaved: () => void;
  onToast: (msg: string) => void;
}

const MIN_FONT_SIZE = 12;
const MAX_FONT_SIZE = 20;
const DEFAULT_FONT_SIZE = 13;

interface CodeMirrorRuntime {
  Compartment: typeof import('@codemirror/state').Compartment;
  EditorState: typeof import('@codemirror/state').EditorState;
  EditorView: typeof import('@codemirror/view').EditorView;
  baseExtensions: Extension[];
  defaultKeymap: typeof import('@codemirror/commands').defaultKeymap;
  historyKeymap: typeof import('@codemirror/commands').historyKeymap;
  indentWithTab: typeof import('@codemirror/commands').indentWithTab;
  foldKeymap: typeof import('@codemirror/language').foldKeymap;
  searchKeymap: typeof import('@codemirror/search').searchKeymap;
  keymap: typeof import('@codemirror/view').keymap;
  openSearchPanel: typeof import('@codemirror/search').openSearchPanel;
  closeSearchPanel: typeof import('@codemirror/search').closeSearchPanel;
  searchPanelOpen: typeof import('@codemirror/search').searchPanelOpen;
  darkThemeExt: Extension;
  lightThemeExt: Extension;
  editorLanguages: readonly EditorLanguageOption[];
  detectLanguageId: (remotePath: string) => EditorLanguageId;
  loadLanguageExtension: (langId: EditorLanguageId) => Extension;
  detectLineEnding: (text: string) => LineEnding;
  normalizeToLF: (text: string) => string;
  applyLineEnding: (text: string, eol: LineEnding) => string;
}

const AUTO_CLOSE_PAIRS: Readonly<Record<string, string>> = {
  '(': ')',
  '[': ']',
  '{': '}',
  '"': '"',
  "'": "'",
  '`': '`',
};

const AUTO_CLOSE_CLOSERS = new Set(Object.values(AUTO_CLOSE_PAIRS));

let cmRuntimePromise: Promise<CodeMirrorRuntime> | null = null;

function loadCodeMirrorRuntime(): Promise<CodeMirrorRuntime> {
  if (!cmRuntimePromise) {
    cmRuntimePromise = Promise.all([
      import('@codemirror/state'),
      import('@codemirror/view'),
      import('@codemirror/commands'),
      import('@codemirror/language'),
      import('@codemirror/search'),
      import('@lezer/highlight'),
      import('../lib/editorLang'),
    ]).then(([stateMod, viewMod, cmdMod, langMod, searchMod, hlMod, editorLang]) => {
      const { EditorState } = stateMod;
      const {
        EditorView,
        crosshairCursor,
        drawSelection,
        dropCursor,
        highlightActiveLine,
        highlightActiveLineGutter,
        highlightSpecialChars,
        keymap,
        lineNumbers,
        rectangularSelection,
      } = viewMod;
      const { defaultKeymap, history, historyKeymap, indentWithTab } = cmdMod;
      const {
        HighlightStyle,
        bracketMatching,
        foldGutter,
        foldKeymap,
        indentOnInput,
        syntaxHighlighting,
      } = langMod;
      const {
        closeSearchPanel,
        highlightSelectionMatches,
        openSearchPanel,
        search,
        searchKeymap,
        searchPanelOpen,
      } = searchMod;
      const { tags } = hlMod;

      // Lightweight bracket/quote auto-close replacing @codemirror/autocomplete (~37 KB min).
      const lightweightCloseBrackets: Extension = [
        EditorView.inputHandler.of((view, from, to, text) => {
          if (from !== to || text.length !== 1 || view.state.selection.ranges.length !== 1) return false;
          const next = view.state.doc.sliceString(to, to + 1);
          if (AUTO_CLOSE_CLOSERS.has(text) && next === text && (text !== AUTO_CLOSE_PAIRS[text] || from > 0)) {
            view.dispatch({ selection: { anchor: to + 1 }, scrollIntoView: true });
            return true;
          }
          const closer = AUTO_CLOSE_PAIRS[text];
          if (!closer || (next && !/[\s)\]},;:]/.test(next))) return false;
          view.dispatch({
            changes: { from, to, insert: text + closer },
            selection: { anchor: from + 1 },
            scrollIntoView: true,
          });
          return true;
        }),
        keymap.of([
          {
            key: 'Backspace',
            run(view) {
              const sel = view.state.selection.main;
              if (!sel.empty || sel.from === 0 || view.state.selection.ranges.length !== 1) return false;
              const prev = view.state.doc.sliceString(sel.from - 1, sel.from);
              const next = view.state.doc.sliceString(sel.from, sel.from + 1);
              if (AUTO_CLOSE_PAIRS[prev] === next) {
                view.dispatch({ changes: { from: sel.from - 1, to: sel.from + 1 } });
                return true;
              }
              return false;
            },
          },
        ]),
      ];

      const zhPhrases = EditorState.phrases.of({
        Find: '查找',
        Replace: '替换',
        next: '下一个',
        previous: '上一个',
        all: '全选',
        'match case': '区分大小写',
        regexp: '正则',
        'by word': '全字匹配',
        replace: '替换',
        'replace all': '全部替换',
        close: '关闭',
        'Go to line': '跳转到行',
        go: '跳转',
      });

      const darkEditorChrome = EditorView.theme(
        {
          '&': {
            color: 'var(--ink-primary)',
            backgroundColor: 'var(--bg-app)',
          },
          '.cm-content': {
            caretColor: 'var(--accent)',
          },
          '.cm-cursor, .cm-dropCursor': {
            borderLeftColor: 'var(--accent)',
            borderLeftWidth: '1.8px',
          },
          '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection':
            {
              backgroundColor: 'color-mix(in srgb, var(--accent) 28%, transparent)',
            },
          '.cm-activeLine': {
            backgroundColor: 'color-mix(in srgb, var(--ink-primary) 4.2%, transparent)',
          },
          '.cm-gutters': {
            backgroundColor: 'var(--bg-titlebar)',
            color: 'var(--ink-tertiary)',
            borderRight: '1px solid var(--border-hairline)',
          },
          '.cm-activeLineGutter': {
            backgroundColor: 'var(--bg-hover)',
            color: 'var(--ink-primary)',
          },
          '.cm-selectionMatch': {
            backgroundColor: 'color-mix(in srgb, var(--accent) 22%, transparent)',
            borderRadius: '2px',
          },
          '.cm-searchMatch': {
            backgroundColor: 'color-mix(in srgb, var(--orange) 24%, transparent)',
            outline: '1px solid color-mix(in srgb, var(--orange) 52%, transparent)',
            borderRadius: '2px',
          },
          '.cm-searchMatch.cm-searchMatch-selected': {
            backgroundColor: 'color-mix(in srgb, var(--orange) 44%, transparent)',
            outline: '1px solid var(--orange)',
          },
          '&.cm-focused .cm-matchingBracket, &.cm-focused .cm-nonmatchingBracket': {
            backgroundColor: 'color-mix(in srgb, var(--accent) 22%, transparent)',
            outline: '1px solid color-mix(in srgb, var(--accent) 55%, transparent)',
            borderRadius: '2px',
          },
        },
        { dark: true },
      );

      const paletteHighlightStyle = HighlightStyle.define([
        { tag: [tags.comment, tags.lineComment, tags.blockComment, tags.docComment], color: 'var(--syn-comment)', fontStyle: 'italic' },
        { tag: [tags.keyword, tags.controlKeyword, tags.definitionKeyword, tags.moduleKeyword, tags.modifier], color: 'var(--syn-keyword)' },
        { tag: [tags.operator, tags.operatorKeyword, tags.derefOperator], color: 'var(--syn-operator)' },
        { tag: [tags.string, tags.docString, tags.character, tags.attributeValue], color: 'var(--syn-string)' },
        { tag: [tags.regexp, tags.escape, tags.special(tags.string)], color: 'var(--syn-regexp)' },
        { tag: [tags.number, tags.integer, tags.float, tags.bool, tags.null, tags.atom, tags.unit], color: 'var(--syn-number)' },
        { tag: [tags.variableName, tags.self], color: 'var(--ink-primary)' },
        { tag: [tags.definition(tags.variableName), tags.function(tags.variableName), tags.function(tags.propertyName)], color: 'var(--syn-func)' },
        { tag: [tags.propertyName, tags.attributeName], color: 'var(--syn-prop)' },
        { tag: [tags.typeName, tags.className, tags.namespace, tags.macroName], color: 'var(--syn-type)' },
        { tag: [tags.tagName], color: 'var(--syn-tag)' },
        { tag: [tags.labelName, tags.meta, tags.annotation, tags.processingInstruction], color: 'var(--syn-label)' },
        { tag: [tags.punctuation, tags.separator, tags.bracket], color: 'var(--ink-secondary)' },
        { tag: [tags.heading], color: 'var(--syn-func)', fontWeight: '600' },
        { tag: [tags.strong], fontWeight: '600', color: 'var(--ink-primary)' },
        { tag: [tags.emphasis], fontStyle: 'italic', color: 'var(--ink-primary)' },
        { tag: [tags.link, tags.url], color: 'var(--accent)', textDecoration: 'underline' },
        { tag: [tags.inserted], color: 'var(--syn-inserted)', backgroundColor: 'color-mix(in srgb, var(--syn-inserted) 14%, transparent)' },
        { tag: [tags.deleted], color: 'var(--syn-deleted)', backgroundColor: 'color-mix(in srgb, var(--syn-deleted) 14%, transparent)' },
        { tag: [tags.changed], color: 'var(--syn-changed)', backgroundColor: 'color-mix(in srgb, var(--syn-changed) 14%, transparent)' },
        { tag: [tags.invalid], color: 'var(--syn-invalid)', textDecoration: 'underline wavy' },
      ]);

      const lightEditorChrome = EditorView.theme(
        {
          '&': {
            color: 'var(--ink-primary)',
            backgroundColor: 'var(--bg-app)',
          },
          '.cm-content': {
            caretColor: 'var(--accent)',
          },
          '.cm-cursor, .cm-dropCursor': {
            borderLeftColor: 'var(--accent)',
            borderLeftWidth: '1.8px',
          },
          '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection':
            {
              backgroundColor: 'color-mix(in srgb, var(--accent) 18%, transparent)',
            },
          '.cm-activeLine': {
            backgroundColor: 'color-mix(in srgb, var(--ink-primary) 4%, transparent)',
          },
          '.cm-gutters': {
            backgroundColor: 'var(--bg-surface-subtle)',
            color: 'var(--ink-tertiary)',
            borderRight: '1px solid var(--border-hairline)',
          },
          '.cm-activeLineGutter': {
            backgroundColor: 'var(--bg-hover)',
            color: 'var(--ink-primary)',
          },
          '.cm-selectionMatch': {
            backgroundColor: 'color-mix(in srgb, var(--accent) 13%, transparent)',
            borderRadius: '2px',
          },
          '.cm-searchMatch': {
            backgroundColor: 'color-mix(in srgb, var(--orange) 24%, transparent)',
            outline: '1px solid color-mix(in srgb, var(--orange) 50%, transparent)',
            borderRadius: '2px',
          },
          '.cm-searchMatch.cm-searchMatch-selected': {
            backgroundColor: 'color-mix(in srgb, var(--orange) 42%, transparent)',
            outline: '1px solid var(--orange)',
          },
          '&.cm-focused .cm-matchingBracket, &.cm-focused .cm-nonmatchingBracket': {
            backgroundColor: 'color-mix(in srgb, var(--accent) 16%, transparent)',
            outline: '1px solid color-mix(in srgb, var(--accent) 45%, transparent)',
            borderRadius: '2px',
          },
        },
        { dark: false },
      );

      const paletteSyntaxExt = syntaxHighlighting(paletteHighlightStyle);

      return {
        Compartment: stateMod.Compartment,
        EditorState,
        EditorView,
        baseExtensions: [
          zhPhrases,
          lineNumbers(),
          highlightActiveLineGutter(),
          highlightSpecialChars(),
          history(),
          editorLang.indentFoldService,
          foldGutter({
            openText: '▾',
            closedText: '▸',
          }),
          drawSelection(),
          dropCursor(),
          EditorState.allowMultipleSelections.of(true),
          indentOnInput(),
          bracketMatching(),
          lightweightCloseBrackets,
          rectangularSelection(),
          crosshairCursor(),
          highlightActiveLine(),
          highlightSelectionMatches(),
          search({ top: true }),
        ],
        defaultKeymap,
        historyKeymap,
        indentWithTab,
        foldKeymap,
        searchKeymap,
        keymap,
        openSearchPanel,
        closeSearchPanel,
        searchPanelOpen,
        darkThemeExt: [darkEditorChrome, paletteSyntaxExt],
        lightThemeExt: [lightEditorChrome, paletteSyntaxExt],
        editorLanguages: editorLang.EDITOR_LANGUAGES,
        detectLanguageId: editorLang.detectLanguageId,
        loadLanguageExtension: editorLang.loadLanguageExtension,
        detectLineEnding: editorLang.detectLineEnding,
        normalizeToLF: editorLang.normalizeToLF,
        applyLineEnding: editorLang.applyLineEnding,
      };
    });
  }
  return cmRuntimePromise;
}

function getFontSizeExtension(EditorViewClass: typeof import('@codemirror/view').EditorView, size: number): Extension {
  return EditorViewClass.theme({
    '&': { fontSize: `${size}px` },
    '.cm-scroller': { fontFamily: 'var(--font-mono)' },
  });
}

function openReplacePanel(rt: CodeMirrorRuntime, view: EditorView): boolean {
  rt.openSearchPanel(view);
  requestAnimationFrame(() => {
    const replaceInput = view.dom.querySelector<HTMLInputElement>('.cm-search input[name="replace"]');
    if (replaceInput) {
      replaceInput.focus();
      replaceInput.select();
    }
  });
  return true;
}

function readIsDarkTheme(): boolean {
  return typeof document !== 'undefined' && document.documentElement.dataset.theme === 'dark';
}

export const RemoteEditorModal: React.FC<RemoteEditorModalProps> = ({
  sessionId,
  remotePath,
  isDark: isDarkProp,
  onClose,
  onSaved,
  onToast,
}) => {
  const [cmRuntime, setCmRuntime] = useState<CodeMirrorRuntime | null>(null);
  const [content, setContent] = useState('');
  const [original, setOriginal] = useState<string | null>(null);
  const [lineEnding, setLineEnding] = useState<LineEnding>('LF');
  const [originalLineEnding, setOriginalLineEnding] = useState<LineEnding>('LF');
  const [loading, setLoading] = useState(true);
  const [reloading, setReloading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState('');
  const [langId, setLangId] = useState<EditorLanguageId>('plain');
  const [wordWrap, setWordWrap] = useState(false);
  const [maximized, setMaximized] = useState(false);
  const [fontSize, setFontSize] = useState(DEFAULT_FONT_SIZE);
  const [domDark, setDomDark] = useState<boolean>(readIsDarkTheme);
  const isDark = isDarkProp ?? domDark;
  const [cursor, setCursor] = useState({ line: 1, col: 1, selected: 0 });
  const [lineCount, setLineCount] = useState(1);
  const [searchOpen, setSearchOpen] = useState(false);
  const [pendingAction, setPendingAction] = useState<null | 'close' | 'reload'>(null);

  const editorHostRef = useRef<HTMLDivElement | null>(null);
  const viewRef = useRef<EditorView | null>(null);
  const runtimeRef = useRef<CodeMirrorRuntime | null>(null);
  const loadSeqRef = useRef(0);
  const savingRef = useRef(false);

  const compartmentsRef = useRef<{
    language: Compartment;
    theme: Compartment;
    wrap: Compartment;
    fontSize: Compartment;
  } | null>(null);

  const dirty = original !== null && (content !== original || lineEnding !== originalLineEnding);

  // Sync live theme changes from document.documentElement[data-theme] when not explicitly controlled
  useEffect(() => {
    if (isDarkProp !== undefined) return;
    const root = document.documentElement;
    const sync = () => setDomDark(root.dataset.theme === 'dark');
    sync();
    const observer = new MutationObserver(sync);
    observer.observe(root, { attributes: true, attributeFilter: ['data-theme', 'data-palette'] });
    return () => observer.disconnect();
  }, [isDarkProp]);

  // Automatically dismiss unsaved confirmation banner if changes are undone or saved
  useEffect(() => {
    if (!dirty && pendingAction !== null) {
      setPendingAction(null);
    }
  }, [dirty, pendingAction]);

  const adjustFontSize = useCallback((delta: number) => {
    setFontSize((prev) => Math.max(MIN_FONT_SIZE, Math.min(MAX_FONT_SIZE, prev + delta)));
  }, []);

  const resetFontSize = useCallback(() => {
    setFontSize(DEFAULT_FONT_SIZE);
  }, []);

  const save = useCallback(
    async (closeAfter = false): Promise<boolean> => {
      const rt = runtimeRef.current;
      if (savingRef.current || loading || reloading || original === null || !rt) return false;
      const isDirtyNow = content !== original || lineEnding !== originalLineEnding;
      if (!isDirtyNow) {
        if (closeAfter) onClose();
        return true;
      }
      savingRef.current = true;
      setSaving(true);
      setErr('');
      try {
        const serialized = rt.applyLineEnding(content, lineEnding);
        await getApi().WriteRemoteText(sessionId, remotePath, serialized);
        setOriginal(content);
        setOriginalLineEnding(lineEnding);
        setPendingAction(null);
        onSaved();
        onToast('已保存到远端服务器');
        if (closeAfter) {
          onClose();
        }
        return true;
      } catch (e) {
        setErr(errorMessage(e));
        return false;
      } finally {
        savingRef.current = false;
        setSaving(false);
      }
    },
    [content, lineEnding, loading, onClose, onSaved, onToast, original, originalLineEnding, reloading, remotePath, sessionId],
  );

  const saveRef = useRef(save);
  saveRef.current = save;

  const adjustFontSizeRef = useRef(adjustFontSize);
  adjustFontSizeRef.current = adjustFontSize;

  const resetFontSizeRef = useRef(resetFontSize);
  resetFontSizeRef.current = resetFontSize;

  const ensureCompartments = useCallback((rt: CodeMirrorRuntime) => {
    if (!compartmentsRef.current) {
      compartmentsRef.current = {
        language: new rt.Compartment(),
        theme: new rt.Compartment(),
        wrap: new rt.Compartment(),
        fontSize: new rt.Compartment(),
      };
    }
    return compartmentsRef.current;
  }, []);

  const buildEditorState = useCallback(
    (
      rt: CodeMirrorRuntime,
      docText: string,
      activeLang: EditorLanguageId,
      dark: boolean,
      wrap: boolean,
      size: number,
    ): EditorState => {
      const comps = ensureCompartments(rt);
      return rt.EditorState.create({
        doc: docText,
        extensions: [
          ...rt.baseExtensions,
          rt.keymap.of([
            {
              key: 'Mod-s',
              preventDefault: true,
              run: () => {
                void saveRef.current(false);
                return true;
              },
            },
            {
              key: 'Mod-h',
              preventDefault: true,
              run: (view) => openReplacePanel(rt, view),
            },
            {
              key: 'Mod-=',
              preventDefault: true,
              run: () => {
                adjustFontSizeRef.current(1);
                return true;
              },
            },
            {
              key: 'Mod--',
              preventDefault: true,
              run: () => {
                adjustFontSizeRef.current(-1);
                return true;
              },
            },
            {
              key: 'Mod-0',
              preventDefault: true,
              run: () => {
                resetFontSizeRef.current();
                return true;
              },
            },
            ...rt.defaultKeymap,
            ...rt.searchKeymap,
            ...rt.historyKeymap,
            ...rt.foldKeymap,
            rt.indentWithTab,
          ]),
          comps.language.of(rt.loadLanguageExtension(activeLang)),
          comps.theme.of(dark ? rt.darkThemeExt : rt.lightThemeExt),
          comps.wrap.of(wrap ? rt.EditorView.lineWrapping : []),
          comps.fontSize.of(getFontSizeExtension(rt.EditorView, size)),
          rt.EditorView.updateListener.of((update) => {
            if (update.docChanged) {
              setContent(update.state.doc.toString());
              setLineCount(update.state.doc.lines);
            }
            if (update.selectionSet || update.docChanged) {
              const sel = update.state.selection.main;
              const line = update.state.doc.lineAt(sel.head);
              setCursor({
                line: line.number,
                col: sel.head - line.from + 1,
                selected: Math.abs(sel.to - sel.from),
              });
            }
            const panelOpen = rt.searchPanelOpen(update.state);
            setSearchOpen((prev) => (prev === panelOpen ? prev : panelOpen));
          }),
        ],
      });
    },
    [ensureCompartments],
  );

  // Refs to read latest visual preferences when resetting state on load/reload without re-triggering fetch
  const prefsRef = useRef({ langId, isDark, wordWrap, fontSize });
  prefsRef.current = { langId, isDark, wordWrap, fontSize };

  const loadFile = useCallback(
    async (mode: 'initial' | 'reload') => {
      const seq = ++loadSeqRef.current;
      if (mode === 'initial') {
        setLoading(true);
        setOriginal(null);
        setContent('');
        setPendingAction(null);
      } else {
        setReloading(true);
      }
      setErr('');
      try {
        const [rt, raw] = await Promise.all([
          loadCodeMirrorRuntime(),
          getApi().ReadRemoteText(sessionId, remotePath),
        ]);
        if (loadSeqRef.current !== seq) return;

        runtimeRef.current = rt;
        setCmRuntime(rt);

        const detectedEol = rt.detectLineEnding(raw);
        const normalized = rt.normalizeToLF(raw);
        const nextLang = mode === 'initial' ? rt.detectLanguageId(remotePath) : prefsRef.current.langId;

        setLangId(nextLang);
        setLineEnding(detectedEol);
        setOriginalLineEnding(detectedEol);
        setContent(normalized);
        setOriginal(normalized);
        setLineCount(normalized.split('\n').length);
        setCursor({ line: 1, col: 1, selected: 0 });
        setPendingAction(null);
        setSearchOpen(false);

        if (viewRef.current) {
          const { isDark: dark, wordWrap: wrap, fontSize: size } = prefsRef.current;
          viewRef.current.setState(buildEditorState(rt, normalized, nextLang, dark, wrap, size));
          viewRef.current.focus();
        }
        if (mode === 'reload') {
          onToast('已从远端重新加载');
        }
      } catch (e) {
        if (loadSeqRef.current === seq) {
          setErr(errorMessage(e));
        }
      } finally {
        if (loadSeqRef.current === seq) {
          setLoading(false);
          setReloading(false);
        }
      }
    },
    [buildEditorState, onToast, remotePath, sessionId],
  );

  // Initial load on sessionId / remotePath change
  useEffect(() => {
    void loadFile('initial');
  }, [loadFile]);

  // Mount CodeMirror EditorView once runtime and file content are ready
  const editorReady = !loading && original !== null && cmRuntime !== null;
  useEffect(() => {
    if (!editorReady || !editorHostRef.current || !cmRuntime || original === null) return;
    if (viewRef.current) return;

    const { langId: activeLang, isDark: dark, wordWrap: wrap, fontSize: size } = prefsRef.current;
    const view = new cmRuntime.EditorView({
      state: buildEditorState(cmRuntime, original, activeLang, dark, wrap, size),
      parent: editorHostRef.current,
    });
    viewRef.current = view;
    view.focus();

    return () => {
      view.destroy();
      viewRef.current = null;
    };
    // Mount once when editorReady transitions true; subsequent reloads reuse view.setState
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editorReady]);

  // Live reconfigure compartments without recreating EditorView
  useEffect(() => {
    const view = viewRef.current;
    const rt = runtimeRef.current;
    const comps = compartmentsRef.current;
    if (!view || !rt || !comps) return;
    view.dispatch({ effects: comps.language.reconfigure(rt.loadLanguageExtension(langId)) });
  }, [langId]);

  useEffect(() => {
    const view = viewRef.current;
    const rt = runtimeRef.current;
    const comps = compartmentsRef.current;
    if (!view || !rt || !comps) return;
    view.dispatch({ effects: comps.theme.reconfigure(isDark ? rt.darkThemeExt : rt.lightThemeExt) });
  }, [isDark]);

  useEffect(() => {
    const view = viewRef.current;
    const rt = runtimeRef.current;
    const comps = compartmentsRef.current;
    if (!view || !rt || !comps) return;
    view.dispatch({ effects: comps.wrap.reconfigure(wordWrap ? rt.EditorView.lineWrapping : []) });
  }, [wordWrap]);

  useEffect(() => {
    const view = viewRef.current;
    const rt = runtimeRef.current;
    const comps = compartmentsRef.current;
    if (!view || !rt || !comps) return;
    view.dispatch({ effects: comps.fontSize.reconfigure(getFontSizeExtension(rt.EditorView, fontSize)) });
  }, [fontSize]);

  const requestClose = useCallback(() => {
    if (savingRef.current) return;
    if (dirty) {
      setPendingAction('close');
      return;
    }
    onClose();
  }, [dirty, onClose]);

  const requestReload = useCallback(() => {
    if (loading || reloading || savingRef.current) return;
    if (dirty) {
      setPendingAction('reload');
      return;
    }
    void loadFile('reload');
  }, [dirty, loadFile, loading, reloading]);

  const toggleSearch = useCallback(() => {
    const view = viewRef.current;
    const rt = runtimeRef.current;
    if (!view || !rt) return;
    if (rt.searchPanelOpen(view.state)) {
      rt.closeSearchPanel(view);
      view.focus();
    } else {
      rt.openSearchPanel(view);
    }
  }, []);

  const toggleLineEnding = useCallback(() => {
    if (loading || reloading || original === null) return;
    setLineEnding((prev) => (prev === 'LF' ? 'CRLF' : 'LF'));
  }, [loading, original, reloading]);

  // Window-level keyboard fallback (Ctrl+S, Ctrl+F, Ctrl+H, Esc)
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      const mod = e.ctrlKey || e.metaKey;
      const key = e.key.toLowerCase();
      const rt = runtimeRef.current;
      const view = viewRef.current;

      if (mod && key === 's') {
        if (!e.defaultPrevented) {
          e.preventDefault();
          void saveRef.current(false);
        }
        return;
      }

      if (mod && key === 'f') {
        if (!e.defaultPrevented && view && rt) {
          e.preventDefault();
          rt.openSearchPanel(view);
        }
        return;
      }

      if (mod && key === 'h') {
        if (!e.defaultPrevented && view && rt) {
          e.preventDefault();
          openReplacePanel(rt, view);
        }
        return;
      }

      if (e.key === 'Escape') {
        // Respect Radix Select popover or CodeMirror's own Escape handling (e.g. closing search)
        if (e.defaultPrevented) return;
        if (document.querySelector('[data-radix-popper-content-wrapper]')) return;

        if (view && rt && rt.searchPanelOpen(view.state)) {
          e.preventDefault();
          rt.closeSearchPanel(view);
          view.focus();
          return;
        }

        if (pendingAction !== null) {
          e.preventDefault();
          setPendingAction(null);
          view?.focus();
          return;
        }

        e.preventDefault();
        requestClose();
      }
    };

    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [pendingAction, requestClose]);

  const fileName = remotePath.split('/').pop() || remotePath;
  const languageOptions = useMemo(
    () => (cmRuntime ? cmRuntime.editorLanguages.map((l) => ({ value: l.id, label: l.label })) : [{ value: 'plain', label: 'Plain Text' }]),
    [cmRuntime],
  );
  const formattedBytes = useMemo(() => {
    const serialized = cmRuntime ? cmRuntime.applyLineEnding(content, lineEnding) : content;
    return formatBytes(new TextEncoder().encode(serialized).length);
  }, [cmRuntime, content, lineEnding]);

  return (
    <div className="modal-backdrop remote-editor-backdrop" onClick={requestClose}>
      <div
        className={`modal-sheet editor remote-editor-sheet ${maximized ? 'is-maximized' : ''}`}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="remote-editor-header">
          <div className="remote-editor-title-wrap">
            <FileCode2 size={16} className="remote-editor-file-icon" />
            <div className="remote-editor-title-meta">
              <h2 className="remote-editor-filename-row">
                <span className="remote-editor-filename" title={fileName}>
                  {fileName}
                </span>
                {dirty && <span className="remote-editor-dirty-dot" title="有未保存更改" />}
              </h2>
              <div className="mono remote-editor-path" title={remotePath}>
                {remotePath}
              </div>
            </div>
          </div>

          <div className="remote-editor-actions">
            <button
              type="button"
              className={`remote-editor-tool-btn ${searchOpen ? 'active' : ''}`}
              disabled={!editorReady}
              onClick={toggleSearch}
              title="查找与替换 (Ctrl+F / Ctrl+H)"
            >
              <Search size={14} />
              <span>查找</span>
            </button>

            <button
              type="button"
              className={`remote-editor-tool-btn ${wordWrap ? 'active' : ''}`}
              disabled={!editorReady}
              onClick={() => setWordWrap((w) => !w)}
              title="切换自动换行"
            >
              <WrapText size={14} />
              <span>自动换行</span>
            </button>

            <button
              type="button"
              className="remote-editor-tool-btn"
              disabled={loading || reloading || saving}
              onClick={requestReload}
              title="从远端服务器重新读取"
            >
              <RefreshCw size={14} className={reloading ? 'spin' : ''} />
              <span>重载</span>
            </button>

            <button
              type="button"
              className="remote-editor-tool-btn icon-only"
              onClick={() => setMaximized((m) => !m)}
              title={maximized ? '还原窗口大小' : '最大化窗口'}
              aria-label={maximized ? '还原窗口大小' : '最大化窗口'}
            >
              {maximized ? <Minimize2 size={14} /> : <Maximize2 size={14} />}
            </button>

            <span className="remote-editor-divider" aria-hidden="true" />

            <button
              type="button"
              className="btn btn-primary remote-editor-save-btn"
              disabled={saving || loading || reloading || !dirty}
              onClick={() => void save(false)}
              title="保存到远端服务器 (Ctrl+S)"
            >
              <Save size={13} />
              {saving ? '保存中…' : '保存'}
            </button>

            <button
              type="button"
              className="remote-editor-tool-btn icon-only"
              onClick={requestClose}
              title="关闭 (Esc)"
              aria-label="关闭"
            >
              <X size={15} />
            </button>
          </div>
        </div>

        {pendingAction !== null && (
          <div className="remote-editor-guard-banner" role="alert">
            <div className="remote-editor-guard-msg">
              <AlertTriangle size={15} className="remote-editor-guard-icon" />
              <span>
                {pendingAction === 'close'
                  ? '当前文件有未保存的更改'
                  : '重新加载将覆盖当前未保存的更改'}
              </span>
            </div>
            <div className="remote-editor-guard-actions">
              {pendingAction === 'close' ? (
                <>
                  <button
                    type="button"
                    className="btn btn-primary"
                    disabled={saving}
                    onClick={() => void save(true)}
                  >
                    {saving ? '保存中…' : '保存并关闭'}
                  </button>
                  <button
                    type="button"
                    className="btn btn-danger"
                    disabled={saving}
                    onClick={onClose}
                  >
                    放弃更改
                  </button>
                  <button
                    type="button"
                    className="btn btn-ghost"
                    disabled={saving}
                    onClick={() => {
                      setPendingAction(null);
                      viewRef.current?.focus();
                    }}
                  >
                    继续编辑
                  </button>
                </>
              ) : (
                <>
                  <button
                    type="button"
                    className="btn btn-danger"
                    disabled={reloading}
                    onClick={() => {
                      setPendingAction(null);
                      void loadFile('reload');
                    }}
                  >
                    放弃并重载
                  </button>
                  <button
                    type="button"
                    className="btn btn-ghost"
                    onClick={() => {
                      setPendingAction(null);
                      viewRef.current?.focus();
                    }}
                  >
                    继续编辑
                  </button>
                </>
              )}
            </div>
          </div>
        )}

        {err && (
          <div className="remote-editor-error-banner" role="alert">
            <p className="error-text">{err}</p>
            <button
              type="button"
              className="remote-editor-tool-btn icon-only"
              onClick={() => setErr('')}
              aria-label="关闭错误提示"
            >
              <X size={13} />
            </button>
          </div>
        )}

        <div className="remote-editor-body">
          {loading ? (
            <div className="remote-editor-state-box">
              <Loader2 size={20} className="spin" />
              <span>正在读取远端文件…</span>
            </div>
          ) : editorReady ? (
            <>
              <div ref={editorHostRef} className="remote-editor-cm-host" />
              {reloading && (
                <div className="remote-editor-reloading-pill">
                  <Loader2 size={12} className="spin" />
                  <span>正在重新加载…</span>
                </div>
              )}
            </>
          ) : (
            <div className="remote-editor-state-box">
              <span>无法读取该远端文件</span>
              <button
                type="button"
                className="btn btn-sm"
                onClick={() => void loadFile('initial')}
              >
                <RefreshCw size={13} />
                重试读取
              </button>
            </div>
          )}
        </div>

        <div className="remote-editor-footer">
          <div className="remote-editor-status-left mono">
            <span>
              行 {cursor.line}, 列 {cursor.col}
              {cursor.selected > 0 ? ` (已选 ${cursor.selected})` : ''}
            </span>
            <span className="remote-editor-status-sep">·</span>
            <span>{lineCount} 行</span>
            <span className="remote-editor-status-sep">·</span>
            <span>{formattedBytes}</span>
          </div>

          <div className="remote-editor-status-right">
            <div className="remote-editor-font-stepper" title="调整字号 (12–20px，Ctrl+/Ctrl-/Ctrl+0)">
              <button
                type="button"
                className="remote-editor-font-btn"
                disabled={!editorReady || fontSize <= MIN_FONT_SIZE}
                onClick={() => adjustFontSize(-1)}
                aria-label="减小字号"
              >
                −
              </button>
              <span className="remote-editor-font-val mono">{fontSize}px</span>
              <button
                type="button"
                className="remote-editor-font-btn"
                disabled={!editorReady || fontSize >= MAX_FONT_SIZE}
                onClick={() => adjustFontSize(1)}
                aria-label="增大字号"
              >
                +
              </button>
            </div>

            <button
              type="button"
              className="remote-editor-status-pill"
              disabled={!editorReady}
              onClick={toggleLineEnding}
              title={`当前换行符：${lineEnding}，点击切换为 ${lineEnding === 'LF' ? 'CRLF' : 'LF'}`}
            >
              {lineEnding}
            </button>

            <Select
              size="sm"
              className="remote-editor-lang-select"
              value={langId}
              onValueChange={(next) => setLangId(next as EditorLanguageId)}
              options={languageOptions}
              disabled={!editorReady}
              ariaLabel="切换语法高亮语言"
            />

            <span className="remote-editor-encoding mono">UTF-8</span>
            <span className="remote-editor-status-sep remote-editor-atomic-hint">·</span>
            <span className="remote-editor-atomic-hint">原子写入并保留原权限</span>
          </div>
        </div>
      </div>
    </div>
  );
};
