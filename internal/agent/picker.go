package agent

import (
	"context"
	"sort"
	"strings"

	"github.com/bigknoxy/joshbot/internal/bus"
)

// Choice is one entry a user can switch to from a picker: the spec to pass to
// the command (`/model <Spec>`, `/personality <Spec>`), a label to show, and
// whether it is the session's current selection. It is the channel-neutral
// form of the /model and /personality lists, so a channel with inline
// buttons can render the same entries the text lists name, and a press cannot
// resolve to something the typed command would refuse.
type Choice struct {
	Spec    string
	Label   string
	Current bool
}

// ModelChoices lists the models the sender of msg can switch to, with the
// session's effective model marked current. The specs are exactly what
// resolveModelSpec accepts, in the order modelList prints them.
func (a *Agent) ModelChoices(ctx context.Context, msg bus.InboundMessage) ([]Choice, error) {
	sess, err := a.sessions.GetOrCreate(ctx, getSessionKey(msg))
	if err != nil {
		return nil, err
	}
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()

	active := a.modelForSessionLocked(sess)
	var out []Choice
	if a.cfg.UseModelsConfig() {
		for _, m := range a.cfg.ModelsConfig.Models {
			if m.Disabled {
				continue
			}
			out = append(out, Choice{Spec: m.Name, Label: m.Name + " · " + m.Model, Current: m.Name == active})
		}
		return out, nil
	}
	names := make([]string, 0, len(a.cfg.Providers))
	for name, p := range a.cfg.Providers {
		if p.Enabled && p.APIKey != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		model := a.cfg.Providers[name].Model
		if model == "" {
			model = a.cfg.Agents.Defaults.Model
		}
		out = append(out, Choice{
			Spec:    name,
			Label:   name + " · " + model,
			Current: active == name || strings.HasPrefix(active, name+":"),
		})
	}
	return out, nil
}

// PersonalityChoices lists the named personality presets plus "none", with
// the session's current one marked. A custom instruction set by
// `/personality <text>` matches no preset and so marks nothing.
func (a *Agent) PersonalityChoices(ctx context.Context, msg bus.InboundMessage) ([]Choice, error) {
	sess, err := a.sessions.GetOrCreate(ctx, getSessionKey(msg))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(personalityPresets))
	for name := range personalityPresets {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Choice, 0, len(names)+1)
	for _, name := range names {
		out = append(out, Choice{Spec: name, Label: name, Current: sess.Personality == personalityPresets[name]})
	}
	out = append(out, Choice{Spec: "none", Label: "none", Current: sess.Personality == ""})
	return out, nil
}
