package service

import (
	"atlas-render/internal/model"
	"atlas-render/internal/persist"
	"atlas-render/internal/scene"
	"time"
)

type Service struct{ Store *persist.Store }

// PutScene replaces a scene document. Component pins are validated against the
// live registry, and a disabled component may keep its current pins but cannot
// be newly introduced by an edit. Unpinned components are never re-resolved
// here: an update becomes visible only through explicit adoption.
func (s *Service) PutScene(id, name string, expected int, d model.Document) (model.Scene, error) {
	var out model.Scene
	if !scene.ValidID(id) || !scene.TrimmedName(name) {
		return out, model.Invalid("invalid scene id or name")
	}
	e := s.Store.Update(func(st *model.State) error {
		if e := scene.Validate(d, st.Components); e != nil {
			return e
		}
		old, ok := st.Scenes[id]
		if old.Revision != expected {
			return model.Conflict("scene revision changed")
		}
		if ok && old.State != "active" {
			return model.Conflict("scene is archived")
		}
		// Component pins are compared as resolved closures, so a disabled
		// component cannot be introduced directly or through a nested
		// revision. Existing pins survive edits and can still be frozen.
		previousPins := map[string]model.PinnedComponent{}
		if ok {
			previousPins, _, _ = scene.Closure(old.Document, st.Components)
		}
		nextPins, refs, e := scene.Closure(d, st.Components)
		if e != nil {
			return e
		}
		if disabled := scene.NewDisabledPins(previousPins, nextPins, st.Components); len(disabled) > 0 {
			return model.Conflictf("component %s is disabled", disabled[0])
		}
		// Moving an existing instance to another revision through a plain
		// document edit would bypass adoption. Component updates only become
		// visible through Adopt; composing a new instance at a chosen revision
		// is still ordinary authoring.
		if ok {
			previousPinsByLayer := map[string]string{}
			var recordPins func([]model.Layer)
			recordPins = func(ls []model.Layer) {
				for _, l := range ls {
					if l.Kind == "component" {
						previousPinsByLayer[l.ID] = scene.PinKey(l.ComponentID, l.ComponentRevision)
					}
					recordPins(l.Children)
				}
			}
			recordPins(old.Document.Layers)
			var checkPins func([]model.Layer) error
			checkPins = func(ls []model.Layer) error {
				for _, l := range ls {
					if l.Kind == "component" {
						if previous, exists := previousPinsByLayer[l.ID]; exists && previous != scene.PinKey(l.ComponentID, l.ComponentRevision) {
							return model.Conflictf("component %s pin changes must use adoption", l.ComponentID)
						}
					}
					if e := checkPins(l.Children); e != nil {
						return e
					}
				}
				return nil
			}
			if e := checkPins(d.Layers); e != nil {
				return e
			}
		}
		for _, ref := range refs {
			a, ok := st.Assets[ref]
			if !ok || a.State != "available" {
				return model.Conflict("scene references unavailable asset")
			}
		}
		out = model.Scene{ID: id, Name: name, Revision: old.Revision + 1, State: "active", Document: d, Adoptions: old.Adoptions, UpdatedAt: time.Now().UTC()}
		st.Scenes[id] = out
		st.Emit("scene.saved", id)
		return nil
	})
	return out, e
}
func (s *Service) SceneState(id string, expected int, target string) (model.Scene, error) {
	var out model.Scene
	if target != "active" && target != "archived" {
		return out, model.Invalid("unknown scene state")
	}
	e := s.Store.Update(func(st *model.State) error {
		v, ok := st.Scenes[id]
		if !ok {
			return model.Missing("scene not found")
		}
		if v.Revision != expected {
			return model.Conflict("scene revision changed")
		}
		if v.State == target {
			out = v
			return nil
		}
		v.State = target
		v.Revision++
		v.UpdatedAt = time.Now().UTC()
		st.Scenes[id] = v
		st.Emit("scene."+target, id)
		out = v
		return nil
	})
	return out, e
}

// Freeze captures the scene revision together with the complete closure of
// assets and component pins in one state transaction. Later component updates,
// disables or asset retirements do not change the frozen snapshot.
func (s *Service) Freeze(id string, expected int) (model.Snapshot, error) {
	var out model.Snapshot
	e := s.Store.Update(func(st *model.State) error {
		v, ok := st.Scenes[id]
		if !ok {
			return model.Missing("scene not found")
		}
		if v.Revision != expected || v.State != "active" {
			return model.Conflict("scene revision or state changed")
		}
		pins, refs, e := scene.Closure(v.Document, st.Components)
		if e != nil {
			return e
		}
		for _, p := range pins {
			if c := st.Components[p.ID]; c.State == "disabled" {
				return model.Conflictf("snapshot references disabled component %s", p.ID)
			}
		}
		assets := map[string]model.Asset{}
		for _, ref := range refs {
			a, ok := st.Assets[ref]
			if !ok || a.State != "available" {
				return model.Conflict("snapshot references unavailable asset")
			}
			assets[ref] = a
		}
		out = model.Snapshot{ID: model.ID("snap", st.Sequence+1), SceneID: id, Revision: v.Revision, State: "active", Document: v.Document, Assets: assets, Components: pins, CreatedAt: time.Now().UTC()}
		out.Digest = model.SnapshotDigest(out)
		st.Snapshots[out.ID] = out
		st.Emit("snapshot.frozen", out.ID)
		return nil
	})
	return out, e
}
func (s *Service) Revoke(id string) error {
	return s.Store.Update(func(st *model.State) error {
		v, ok := st.Snapshots[id]
		if !ok {
			return model.Missing("snapshot not found")
		}
		if v.State == "revoked" {
			return nil
		}
		v.State = "revoked"
		st.Snapshots[id] = v
		st.Emit("snapshot.revoked", id)
		return nil
	})
}
