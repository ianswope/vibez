//go:build darwin && !cgo

package local

import (
	"fmt"
	"time"

	"github.com/simone-vibes/vibez/internal/player"
	"github.com/simone-vibes/vibez/internal/provider"
)

var errNoCgo = fmt.Errorf("local playback requires CGo on macOS. Build with CGO_ENABLED=1")

// Player is a stub so darwin builds without cgo still compile.
// New always fails, so no method is ever reached at runtime.
type Player struct{}

func New() (*Player, error) { return nil, errNoCgo }

func (p *Player) Play() error                          { return errNoCgo }
func (p *Player) Pause() error                         { return errNoCgo }
func (p *Player) Stop() error                          { return errNoCgo }
func (p *Player) Next() error                          { return errNoCgo }
func (p *Player) Previous() error                      { return errNoCgo }
func (p *Player) Seek(_ time.Duration) error           { return errNoCgo }
func (p *Player) SetVolume(_ float64) error            { return errNoCgo }
func (p *Player) SetAudioBitrate(_ int) error          { return errNoCgo }
func (p *Player) SetQueue(_ []string) error            { return errNoCgo }
func (p *Player) SetPlaylist(_ string, _ int) error    { return errNoCgo }
func (p *Player) AppendQueue(_ []string) error         { return errNoCgo }
func (p *Player) SetRepeat(_ int) error                { return errNoCgo }
func (p *Player) SetShuffle(_ bool) error              { return errNoCgo }
func (p *Player) SetEqualizer(_ []player.EQBand) error { return errNoCgo }
func (p *Player) RemoveFromQueue(_ int) error          { return errNoCgo }
func (p *Player) MoveInQueue(_, _ int) error           { return errNoCgo }
func (p *Player) ClearQueue() error                    { return errNoCgo }
func (p *Player) GetState() (*player.State, error)     { return nil, errNoCgo }
func (p *Player) Subscribe() <-chan player.State       { return nil }
func (p *Player) Close() error                         { return nil }
func (p *Player) LoadTracks(_ []provider.Track)        {}
