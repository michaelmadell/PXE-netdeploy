import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { xml } from "@codemirror/lang-xml";
import { bracketMatching, foldGutter, foldKeymap, HighlightStyle, indentOnInput, syntaxHighlighting } from "@codemirror/language";
import { highlightSelectionMatches, search, searchKeymap } from "@codemirror/search";
import { EditorState } from "@codemirror/state";
import {
  Decoration,
  drawSelection,
  EditorView,
  highlightActiveLine,
  highlightActiveLineGutter,
  keymap,
  lineNumbers,
  MatchDecorator,
  ViewPlugin,
  type DecorationSet,
  type ViewUpdate,
} from "@codemirror/view";
import { tags as t } from "@lezer/highlight";
import { forwardRef, useEffect, useImperativeHandle, useRef } from "react";
import { cn } from "@/lib/utils";

// XML (CodeMirror 6) with the Go template actions ({{ … }}) marked. Colours
// come from --cm-* tokens in index.css, so light and dark both work.

const highlight = HighlightStyle.define([
  { tag: [t.tagName, t.angleBracket], color: "var(--cm-tag)" },
  { tag: t.attributeName, color: "var(--cm-attr)" },
  { tag: t.attributeValue, color: "var(--cm-value)" },
  { tag: [t.comment, t.processingInstruction, t.documentMeta], color: "var(--cm-comment)", fontStyle: "italic" },
  { tag: [t.character, t.string], color: "var(--cm-value)" },
]);

const actions = new MatchDecorator({ regexp: /\{\{[\s\S]*?\}\}/g, decoration: Decoration.mark({ class: "cm-nd-action" }) });
const markActions = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    constructor(view: EditorView) {
      this.decorations = actions.createDeco(view);
    }
    update(u: ViewUpdate) {
      this.decorations = actions.updateDeco(u, this.decorations);
    }
  },
  { decorations: (v) => v.decorations },
);

const theme = EditorView.theme({
  "&": { height: "100%", fontSize: "13px", backgroundColor: "var(--card)", color: "var(--foreground)" },
  "&.cm-focused": { outline: "2px solid var(--ring)", outlineOffset: "-1px" },
  ".cm-scroller": { fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace", lineHeight: "1.55" },
  ".cm-gutters": { backgroundColor: "var(--muted)", color: "var(--muted-foreground)", border: "none" },
  ".cm-activeLine": { backgroundColor: "color-mix(in oklab, var(--accent) 45%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "var(--accent)" },
  ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": { backgroundColor: "color-mix(in oklab, var(--primary) 25%, transparent) !important" },
  ".cm-cursor": { borderLeftColor: "var(--foreground)" },
  ".cm-nd-action": { color: "var(--cm-action)", backgroundColor: "color-mix(in oklab, var(--cm-action) 12%, transparent)", borderRadius: "3px" },
  ".cm-panels": { backgroundColor: "var(--muted)", color: "var(--foreground)" },
  ".cm-textfield": { backgroundColor: "var(--card)", border: "1px solid var(--input)", borderRadius: "4px" },
  ".cm-button": { backgroundImage: "none", backgroundColor: "var(--card)", border: "1px solid var(--input)", borderRadius: "4px" },
});

export type CodeEditorHandle = {
  /** Selects line n (1-based), scrolls to it and focuses the editor. */
  goToLine: (n: number) => void;
};

type Props = {
  value: string;
  onChange?: (v: string) => void;
  readOnly?: boolean;
  /** Accessible name of the text area. */
  label: string;
  className?: string;
};

/** A code editor for unattend templates and rendered unattend files. */
export const CodeEditor = forwardRef<CodeEditorHandle, Props>(function CodeEditor({ value, onChange, readOnly, label, className }, ref) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const changed = useRef(onChange);
  changed.current = onChange;

  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          highlightActiveLineGutter(),
          foldGutter(),
          history(),
          drawSelection(),
          indentOnInput(),
          bracketMatching(),
          highlightActiveLine(),
          highlightSelectionMatches(),
          search({ top: true }),
          keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap, ...foldKeymap, ...(readOnly ? [] : [indentWithTab])]),
          xml(),
          syntaxHighlighting(highlight),
          markActions,
          theme,
          EditorState.tabSize.of(2),
          EditorState.readOnly.of(!!readOnly),
          EditorView.contentAttributes.of({ "aria-label": label, ...(readOnly ? { "aria-readonly": "true" } : {}) }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) changed.current?.(u.state.doc.toString());
          }),
        ],
      }),
    });
    view.current = v;
    return () => {
      v.destroy();
      view.current = null;
    };
    // The editor is created once; value changes are applied below.
  }, [readOnly, label]);

  // Follow outside changes (a reset, a new preview) without disturbing typing.
  useEffect(() => {
    const v = view.current;
    if (!v) return;
    const cur = v.state.doc.toString();
    if (cur !== value) v.dispatch({ changes: { from: 0, to: cur.length, insert: value } });
  }, [value]);

  useImperativeHandle(ref, () => ({
    goToLine(n: number) {
      const v = view.current;
      if (!v) return;
      const line = v.state.doc.line(Math.max(1, Math.min(n, v.state.doc.lines)));
      v.dispatch({ selection: { anchor: line.from, head: line.to }, scrollIntoView: true });
      v.focus();
    },
  }));

  return <div ref={host} className={cn("overflow-hidden rounded-md border border-input", className)} />;
});
