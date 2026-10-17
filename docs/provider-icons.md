# Provider icons

Bifrost keeps provider marks configurable with `ui.icons`:

- `unicode` (the default) keeps the original portable symbols.
- `nerd` emits verified Nerd Fonts private-use glyphs where the current
  catalogue has a provider mark, and a readable ASCII abbreviation where it
  does not.
- `ascii` emits readable abbreviations such as `OAI`, `ANT`, `GEM`, and `OR`.

Set the mode with `bifrost config set ui.icons nerd` (or `unicode`/`ascii`).
Unknown values are rejected by settings validation. An omitted value remains
backwards compatible with `unicode`.

The Nerd mode mappings come from the Nerd Fonts glyph catalogue and generated
CSS (current release 3.5.1):

| Provider | Nerd Fonts name | Code point |
| --- | --- | --- |
| OpenAI, Responses, ChatGPT | `nf-cod-openai` | U+EC81 |
| Claude, Anthropic | `nf-cod-claude` | U+EC82 |
| Google | `nf-dev-google` | U+E7F0 |

Sources: [Nerd Fonts `glyphnames.json`](https://raw.githubusercontent.com/ryanoasis/nerd-fonts/master/glyphnames.json),
[generated glyph CSS](https://raw.githubusercontent.com/ryanoasis/nerd-fonts/master/css/nerd-fonts-generated.css),
and the [Nerd Fonts v3.5.1 release notes](https://www.nerdfonts.com/releases).
The original [Nerd Fonts issue #2001](https://github.com/ryanoasis/nerd-fonts/issues/2001)
requested Font Awesome Claude/OpenAI brands in April 2026; the current
generated catalogue verifies the released `nf-cod-*` marks above.
The v3.5.0 release updated Codicons to 0.0.45; the mappings above are taken
from the resulting v3.5.1 catalogue.

These are private-use characters, not Unicode-standard logos. `nerd` requires
the terminal to use a Nerd Font containing the matching Codicons/Devicons
sets (Nerd Fonts v3.5.x or newer is recommended). Bifrost does not detect or
install fonts and cannot make a terminal render a missing glyph; a box or
replacement mark means the terminal font lacks that glyph. Cursor, OpenRouter,
and Gemini currently have no verified provider logo in the catalogue, so Nerd
mode uses `CUR`, `OR`, and `GEM` respectively rather than presenting a
generic shape as a brand logo.
