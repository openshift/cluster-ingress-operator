package managementmode

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStore_UpdateAndRead_concurrent(t *testing.T) {
	store := NewStore(State{EffectiveMode: ModeManaged})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mode := ModeManaged
			if i%2 == 0 {
				mode = ModeUnmanaged
			}
			store.Update(State{EffectiveMode: mode, ObservedGeneration: int64(i)})
			_ = store.Current()
		}(i)
	}
	wg.Wait()
	assert.NotEmpty(t, store.Current().EffectiveMode)
}
