/*
Copyright 2026 Neotera.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package source_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/source"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
)

const (
	tireKey     = "devices/default/automotive-tire-pressure-ecu-fw-001"
	minInterval = 10 * time.Second
)

var start = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

// tire returns a device resource whose front-left pressure is reported as pressure.
func tire(pressure string) model.Resource {
	return model.Resource{
		Kind: model.KindDevice,
		Key:  tireKey,
		Spec: &model.DeviceSpec{
			Namespace: "default",
			Name:      "automotive-tire-pressure-ecu-fw-001",
			Properties: []model.DeviceProperty{{
				Name:     "frontLeftPressure",
				Reported: &model.TwinValue{Value: pressure},
			}},
		},
	}
}

func reportedPressure(t *testing.T, st *store.Store) string {
	t.Helper()
	r, ok := st.Get(tireKey)
	require.True(t, ok)
	return r.Spec.(*model.DeviceSpec).Properties[0].Reported.Value
}

// newThrottle wraps a store in a throttle on a fake clock. The store keeps the real clock:
// FakeClock runs AfterFunc callbacks while holding its lock, so a store reading the fake clock
// from inside a flush would deadlock.
func newThrottle(interval time.Duration) (*source.Throttle, *store.Store, *clocktesting.FakeClock) {
	clk := clocktesting.NewFakeClock(start)
	st := store.New()
	return source.NewThrottle(st, interval, clk), st, clk
}

func TestThrottleHoldsAndPublishesLatest(t *testing.T) {
	th, st, clk := newThrottle(minInterval)

	changed, err := th.Upsert(tire("230.0"))
	require.NoError(t, err)
	assert.True(t, changed, "the first change passes at once")

	clk.Step(2 * time.Second)
	changed, err = th.Upsert(tire("225.0"))
	require.NoError(t, err)
	assert.False(t, changed, "a change within the interval is held")
	changed, err = th.Upsert(tire("220.0"))
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, "230.0", reportedPressure(t, st))
	assert.Equal(t, uint64(1), st.Revision())

	// At the end of the interval only the latest held value is published.
	clk.Step(8 * time.Second)
	assert.Equal(t, "220.0", reportedPressure(t, st))
	assert.Equal(t, uint64(2), st.Revision())

	// The flush started a new interval.
	clk.Step(time.Second)
	changed, err = th.Upsert(tire("215.0"))
	require.NoError(t, err)
	assert.False(t, changed)
	clk.Step(9 * time.Second)
	assert.Equal(t, "215.0", reportedPressure(t, st))
	assert.Equal(t, uint64(3), st.Revision())

	// After a quiet interval a change passes at once again.
	clk.Step(30 * time.Second)
	changed, err = th.Upsert(tire("210.0"))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.False(t, clk.HasWaiters())
}

func TestThrottleNoOpDoesNotStartInterval(t *testing.T) {
	th, st, clk := newThrottle(minInterval)
	_, err := th.Upsert(tire("230.0"))
	require.NoError(t, err)

	// A no-op after a quiet interval is suppressed by the store and starts no interval.
	clk.Step(minInterval)
	changed, err := th.Upsert(tire("230.0"))
	require.NoError(t, err)
	assert.False(t, changed)

	clk.Step(time.Second)
	changed, err = th.Upsert(tire("225.0"))
	require.NoError(t, err)
	assert.True(t, changed, "the last change was 11 s ago; the no-op must not count")
	assert.Equal(t, uint64(2), st.Revision())
}

func TestThrottleDeleteDropsHeldChange(t *testing.T) {
	th, st, clk := newThrottle(minInterval)
	_, err := th.Upsert(tire("230.0"))
	require.NoError(t, err)
	_, err = th.Upsert(tire("225.0"))
	require.NoError(t, err)

	assert.True(t, th.Delete(tireKey))
	_, ok := st.Get(tireKey)
	assert.False(t, ok)

	clk.Step(minInterval)
	_, ok = st.Get(tireKey)
	assert.False(t, ok, "the held change must not resurrect a deleted device")

	// A re-created device passes at once.
	changed, err := th.Upsert(tire("240.0"))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "240.0", reportedPressure(t, st))
}

func TestThrottleRejectsInvalidResourceAtOnce(t *testing.T) {
	th, st, _ := newThrottle(minInterval)
	_, err := th.Upsert(tire("230.0"))
	require.NoError(t, err)

	invalid := tire("225.0")
	invalid.Key = ""
	_, err = th.Upsert(invalid)
	assert.Error(t, err)
	assert.Equal(t, uint64(1), st.Revision())
}

func TestThrottleStop(t *testing.T) {
	th, st, clk := newThrottle(minInterval)
	_, err := th.Upsert(tire("230.0"))
	require.NoError(t, err)
	_, err = th.Upsert(tire("225.0"))
	require.NoError(t, err)

	th.Stop()
	clk.Step(minInterval)
	assert.Equal(t, "230.0", reportedPressure(t, st), "a held change is dropped on stop")

	changed, err := th.Upsert(tire("220.0"))
	require.NoError(t, err)
	assert.False(t, changed, "a stopped throttle ignores upserts")

	assert.True(t, th.Delete(tireKey), "a stopped throttle still passes deletes")
}

func TestThrottleZeroIntervalPassesEverything(t *testing.T) {
	th, st, clk := newThrottle(0)
	for _, p := range []string{"230.0", "225.0", "220.0"} {
		changed, err := th.Upsert(tire(p))
		require.NoError(t, err)
		assert.True(t, changed)
	}
	assert.Equal(t, uint64(3), st.Revision())
	assert.False(t, clk.HasWaiters())
}
