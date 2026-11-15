package bundle

import (
	"atlas-render/internal/model"
	"atlas-render/internal/persist"
	"encoding/json"
	"fmt"
)

type Package struct {
	Format   string         `json:"format"`
	Version  int            `json:"version"`
	Snapshot model.Snapshot `json:"snapshot"`
	Digest   string         `json:"digest"`
}

func Export(s model.Snapshot) Package {
	p := Package{Format: "atlas-scene", Version: 1, Snapshot: s}
	p.Digest = model.CanonicalDigest(p.Snapshot)
	return p
}
func Validate(p Package) error {
	if p.Format != "atlas-scene" || p.Version != 1 {
		return model.Invalid("unsupported scene package")
	}
	if model.CanonicalDigest(p.Snapshot) != p.Digest {
		return model.Invalid("package checksum mismatch")
	}
	// The embedded snapshot digest commits to the document as well as to the
	// complete asset and component closure, so a tampered pin is rejected even
	// when the outer checksum is recomputed by the sender.
	if model.SnapshotDigest(p.Snapshot) != p.Snapshot.Digest {
		return model.Invalid("package snapshot digest mismatch")
	}
	if e := persist.ValidateSnapshot(p.Snapshot); e != nil {
		return e
	}
	return nil
}
func Decode(data []byte) (Package, error) {
	var p Package
	if len(data) > 12<<20 {
		return p, model.Invalid("package exceeds 12MiB")
	}
	if e := json.Unmarshal(data, &p); e != nil {
		return p, fmt.Errorf("decode package: %w", e)
	}
	return p, Validate(p)
}
