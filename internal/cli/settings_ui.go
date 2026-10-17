package cli

import (
	"encoding/json"
	"errors"

	"bifrost/internal/config"
	"bifrost/internal/ui"
)

// Appearance can be safely refreshed without rebuilding a running agent,
// changing its permissions, or replacing its conversation.
func appearanceOptions(settings config.UISettings) []ui.Option {
	layout := settings.Layout
	if layout == "" {
		layout = "studio"
	}
	p := settings.PaneProportions
	options := []ui.Option{
		ui.WithTheme(settings.Theme), ui.WithIcons(settings.Icons),
		ui.WithEditor(settings.Editor), ui.WithLayout(layout),
		ui.WithPaneProportions(p[0], p[1], p[2]),
		ui.WithTerminalPercent(settings.TerminalPercent),
		ui.WithComposer(settings.ComposerMaxRows, settings.NewlineKey),
	}
	if settings.TerminalVisible != nil {
		options = append(options, ui.WithTerminalVisible(*settings.TerminalVisible))
	} else {
		options = append(options, ui.WithTerminalVisible(false))
	}
	return options
}

func settingsDocument(path, revision string, cfg config.Settings) (ui.SettingsDocument, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return ui.SettingsDocument{}, err
	}
	doc := ui.SettingsDocument{Path: path, Revision: revision, Data: string(data)}
	for _, f := range config.EditFields(cfg) {
		doc.Fields = append(doc.Fields, ui.SettingsField{Key: f.Key, Value: f.Value, Kind: f.Kind, Description: f.Description})
	}
	return doc, nil
}

func settingsUIOption(opts options) (ui.Option, error) {
	callbacks, err := settingsUICallbacks(opts)
	if err != nil {
		return nil, err
	}
	return ui.WithSettings(callbacks), nil
}

func settingsUICallbacks(opts options) (ui.SettingsCallbacks, error) {
	path, err := opts.configFile()
	if err != nil {
		return ui.SettingsCallbacks{}, err
	}
	load := func() (ui.SettingsDocument, error) {
		cfg, revision, err := config.LoadSettingsRevision(path)
		if err != nil {
			return ui.SettingsDocument{}, err
		}
		return settingsDocument(path, revision, cfg)
	}
	return ui.SettingsCallbacks{
		Load: load,
		Edit: func(doc ui.SettingsDocument, key, value string) (ui.SettingsDocument, error) {
			var cfg config.Settings
			if err := json.Unmarshal([]byte(doc.Data), &cfg); err != nil {
				return doc, err
			}
			if err := config.SetValue(&cfg, key, value); err != nil {
				return doc, err
			}
			return settingsDocument(path, doc.Revision, cfg)
		},
		Save: func(doc ui.SettingsDocument) (ui.SettingsDocument, []ui.Option, error) {
			var cfg config.Settings
			if err := json.Unmarshal([]byte(doc.Data), &cfg); err != nil {
				return doc, nil, err
			}
			if err := config.SaveSettingsRevision(path, cfg, doc.Revision); err != nil {
				if errors.Is(err, config.ErrRevisionConflict) {
					return doc, nil, errors.New("Config changed on disk. Your draft is retained; discard and reopen settings to reload.")
				}
				return doc, nil, err
			}
			saved, err := load()
			if err != nil {
				return doc, nil, err
			}
			return saved, appearanceOptions(cfg.UI), nil
		},
	}, nil
}
