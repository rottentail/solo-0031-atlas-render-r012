package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const FormatVersion = 1

type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}
type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}
type Transform struct {
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	ScaleX  float64 `json:"scaleX"`
	ScaleY  float64 `json:"scaleY"`
	Degrees float64 `json:"degrees"`
}
type Paint struct {
	Kind     string `json:"kind"`
	Color    string `json:"color"`
	EndColor string `json:"endColor,omitempty"`
	From     Point  `json:"from"`
	To       Point  `json:"to"`
}
type Layer struct {
	ID                string    `json:"id"`
	Kind              string    `json:"kind"`
	Width             float64   `json:"width"`
	Height            float64   `json:"height"`
	Transform         Transform `json:"transform"`
	Opacity           float64   `json:"opacity"`
	Fill              Paint     `json:"fill"`
	Points            []Point   `json:"points,omitempty"`
	AssetID           string    `json:"assetId,omitempty"`
	ComponentID       string    `json:"componentId,omitempty"`
	ComponentRevision int       `json:"componentRevision,omitempty"`
	Clip              *Rect     `json:"clip,omitempty"`
	Children          []Layer   `json:"children,omitempty"`
}
type Document struct {
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Background string  `json:"background"`
	Layers     []Layer `json:"layers"`
}
type ComponentDocument struct {
	Layers []Layer `json:"layers"`
}
type ComponentRevision struct {
	Revision  int               `json:"revision"`
	Document  ComponentDocument `json:"document"`
	CreatedAt time.Time         `json:"createdAt"`
}
type Component struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Revision  int                 `json:"revision"`
	State     string              `json:"state"`
	Revisions []ComponentRevision `json:"revisions"`
	UpdatedAt time.Time           `json:"updatedAt"`
}
type PinnedComponent struct {
	ID       string            `json:"id"`
	Revision int               `json:"revision"`
	Name     string            `json:"name"`
	Document ComponentDocument `json:"document"`
}
type ComponentTarget struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}
type ComponentAdoption struct {
	ComponentID string    `json:"componentId"`
	From        []int     `json:"fromRevisions"`
	To          int       `json:"toRevision"`
	Revision    int       `json:"sceneRevision"`
	At          time.Time `json:"at"`
}
type Scene struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Revision  int                 `json:"revision"`
	State     string              `json:"state"`
	Document  Document            `json:"document"`
	Adoptions []ComponentAdoption `json:"componentAdoptions,omitempty"`
	UpdatedAt time.Time           `json:"updatedAt"`
}
type Asset struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
	State  string `json:"state"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	PNG    []byte `json:"png"`
}
type Snapshot struct {
	ID         string                     `json:"id"`
	SceneID    string                     `json:"sceneId"`
	Revision   int                        `json:"revision"`
	State      string                     `json:"state"`
	Document   Document                   `json:"document"`
	Assets     map[string]Asset           `json:"assets"`
	Components map[string]PinnedComponent `json:"components"`
	Digest     string                     `json:"digest"`
	CreatedAt  time.Time                  `json:"createdAt"`
}
type Job struct {
	ID         string    `json:"id"`
	SnapshotID string    `json:"snapshotId"`
	State      string    `json:"state"`
	Scale      int       `json:"scale"`
	Attempt    int       `json:"attempt"`
	Error      string    `json:"error,omitempty"`
	ArtifactID string    `json:"artifactId,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Payload    Snapshot  `json:"payload"`
}
type Artifact struct {
	ID     string `json:"id"`
	JobID  string `json:"jobId"`
	State  string `json:"state"`
	Digest string `json:"digest"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	PNG    []byte `json:"png,omitempty"`
}
type Receipt struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	JobID       string `json:"jobId"`
}
type Event struct {
	Sequence int       `json:"sequence"`
	Kind     string    `json:"kind"`
	Subject  string    `json:"subject"`
	At       time.Time `json:"at"`
}
type State struct {
	Version    int                  `json:"version"`
	Scenes     map[string]Scene     `json:"scenes"`
	Assets     map[string]Asset     `json:"assets"`
	Components map[string]Component `json:"components"`
	Snapshots  map[string]Snapshot  `json:"snapshots"`
	Jobs       map[string]Job       `json:"jobs"`
	Artifacts  map[string]Artifact  `json:"artifacts"`
	Receipts   map[string]Receipt   `json:"receipts"`
	Events     []Event              `json:"events"`
	Sequence   int                  `json:"sequence"`
}

func NewState() State {
	return State{Version: FormatVersion, Scenes: map[string]Scene{}, Assets: map[string]Asset{}, Components: map[string]Component{}, Snapshots: map[string]Snapshot{}, Jobs: map[string]Job{}, Artifacts: map[string]Artifact{}, Receipts: map[string]Receipt{}}
}
func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func CanonicalDigest(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return Digest(b)
}
func SnapshotDigest(s Snapshot) string {
	// A snapshot without component pins keeps the legacy digest shape so that
	// state written before component support still validates on startup.
	if len(s.Components) == 0 {
		return CanonicalDigest(struct {
			Document Document
			Assets   map[string]Asset
		}{s.Document, s.Assets})
	}
	return CanonicalDigest(struct {
		Document   Document
		Assets     map[string]Asset
		Components map[string]PinnedComponent
	}{s.Document, s.Assets, s.Components})
}
func Clone[T any](v T) T {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	var out T
	if e = json.Unmarshal(b, &out); e != nil {
		panic(e)
	}
	return out
}
func (s *State) Emit(kind, id string) {
	s.Sequence++
	s.Events = append(s.Events, Event{Sequence: s.Sequence, Kind: kind, Subject: id, At: time.Now().UTC()})
	if len(s.Events) > 500 {
		s.Events = s.Events[len(s.Events)-500:]
	}
}
func ID(prefix string, seq int) string { return fmt.Sprintf("%s-%08d", prefix, seq) }
