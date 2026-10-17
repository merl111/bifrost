# Bifrost terminal design

The direction is a dark studio: charcoal backgrounds, violet hierarchy, cyan focus, and restrained amber, green, and red for meaningful state. Color supports labels and symbols; it never carries the only indication of status.

See [navigation.md](navigation.md) for current key behavior, conflicts, and the
proposed navigation increment. Its target bindings are not implemented yet.

## References

- [Crush](https://github.com/charmbracelet/crush): conversational hierarchy, restrained violet accents, compact tool activity, and a prominent composer.
- [Yazi](https://yazi-rs.github.io/features) and its [theme system](https://yazi-rs.github.io/docs/configuration/theme/): readable selection, file navigation, and compact colored status segments.
- [Lazygit](https://github.com/jesseduffield/lazygit): focused panels, keyboard navigation, and keeping review beside its repository context.
- [Claude Code's status line](https://code.claude.com/docs/en/statusline): relevant session information near the prompt. Bifrost puts connection context in a persistent bottom bar.

These are visual and interaction references. Bifrost continues to use its existing Go/Bubble Tea/Lip Gloss implementation; the only addition for rich chat is Goldmark v1.8.5, a Markdown parser with no third-party dependencies.

## Implemented arrangements

| Preset | Reading order | Intended work |
|---|---|---|
| Studio | Files → editor/review → chat | General development. A terminal opens as a viewer tab. |
| Conversation | Files → large chat → inspector | Agent-led work with nearby source context. A terminal opens as a viewer tab. |
| Review | Files → large review/editor → compact chat | Inspecting changes |
| Focus | Chat and composer | Planning and concentrated conversation |

Presets can be switched at runtime. Pane percentages and terminal visibility persist, while the conversation, file selection, and terminal process remain available. Narrow terminals prioritize the active pane and progressively shorten the status bar.

Chat uses an open reading surface with a centered, at-most-88-cell text column and a multiline composer. Surrounding tools retain thin frames, so the conversation is visually distinct. Markdown is parsed before cell-aware wrapping and viewport slicing; complete messages are cached while the streamed message updates. Source messages are never rewritten by presentation. Emoji, skin-tone modifiers, flags, and joined emoji sequences stay intact.

Thin frames define boundaries without making every pane equally prominent. The active heading is cyan; ordinary borders are muted. Chat role headings distinguish user, assistant, tool, status, and error entries. Tool rows start collapsed and expand on click, as do thinking summaries and other activity. Tab/Shift-Tab selects disclosure rows; Enter/Space toggles them. Assistant replies start open but can also be folded. Mouse-wheel scrolling and hit testing use the active layout, viewport offset, and reading-column margins. Fold state is presentation-only and preserved through updates to the same tool call; it is not saved across restarts. Tool detail is bounded to 16 KiB. Provider-supplied thinking content is surfaced after a model step; opaque continuation/signature data is never rendered. Code and Markdown receive semantic color, with a plain theme available. Provider marks are configurable: portable Unicode by default, verified OpenAI/Claude logo glyphs in Nerd Font mode, or ASCII labels. See [font requirements](provider-icons.md).

## Higher apparent resolution

Terminal text is positioned in character cells. Bifrost cannot independently make a text label half a cell tall or change the user's terminal font. A larger window or smaller terminal font produces more cells, and the layout uses those additional rows and columns automatically.

The design is checked at 80×24, 120×36, and 160×48. A 160×48 grid is a useful dense workspace target; it is not a minimum requirement. Fine Unicode borders, restrained spacing, and compact status segments improve precision without requiring special terminal extensions. Choose a monospace font with box-drawing and Unicode coverage in the terminal; an optional icon font may improve glyph coverage but is not required.

Pixel images could later improve browser and VM previews through terminal graphics protocols, with a text fallback for unsupported terminals and SSH/multiplexer combinations. That is separate from text resolution and is not implemented in this increment. Interactive controls and code should remain ordinary terminal text.

## Render evidence

The checked-in ANSI snapshots come from the real view renderer using deterministic fixture data, not an artist's mockup. PNG companions illustrate them with a monospace font; actual font rendering depends on the terminal. Generate ANSI fixtures with:

```sh
UPDATE_UI_SNAPSHOTS=1 go test ./internal/ui -run TestWriteRenderSnapshots -count=1
```

Examples: [Conversation, 160×48](../internal/ui/testdata/snapshots/conversation-160x48.png), [Studio, 120×36](../internal/ui/testdata/snapshots/studio-120x36.png), and [Focus, 160×48](../internal/ui/testdata/snapshots/focus-160x48.png).
