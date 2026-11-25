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

package httpsse_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/model"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/store"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport"
	"github.com/neotera-eu/continuumx/cloud/pkg/dtpublisher/transport/httpsse"
)

const thermalKey = "devices/vehicle-a/automotive-thermal-ecu-fw-001"

// newQueryStore adds a thermal device in another namespace on another node (revision 4) to
// newStore.
func newQueryStore(t *testing.T) *store.Store {
	st := newStore(t)
	mustUpsert(t, st, model.Resource{
		Kind: model.KindDevice,
		Key:  thermalKey,
		Spec: &model.DeviceSpec{Namespace: "vehicle-a", Name: "automotive-thermal-ecu-fw-001", Node: "orin-1"},
	})
	return st
}

type response struct {
	status int
	body   []byte
}

func get(t *testing.T, url string) response {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	var body json.RawMessage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	return response{status: resp.StatusCode, body: body}
}

func (r response) errorBody(t *testing.T) httpsse.ErrorBody {
	t.Helper()
	var e httpsse.ErrorBody
	require.NoError(t, json.Unmarshal(r.body, &e))
	return e
}

func (r response) list(t *testing.T) (httpsse.Cursor, []string) {
	t.Helper()
	require.Equal(t, http.StatusOK, r.status, string(r.body))
	var l struct {
		httpsse.Cursor
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(r.body, &l))
	keys := make([]string, 0, len(l.Items))
	for _, item := range l.Items {
		keys = append(keys, item.Key)
	}
	return l.Cursor, keys
}

func TestQueryDisabledByDefault(t *testing.T) {
	ts := startHTTP(t, newQueryStore(t), httpsse.Options{})
	for _, path := range []string{
		httpsse.NodesPath,
		httpsse.NodesPath + "/u2deb-edge",
		httpsse.DevicesPath + "?node=u2deb-edge",
		httpsse.DevicesPath + "/default/automotive-tire-pressure-ecu-fw-001",
	} {
		r := get(t, ts.URL+path)
		assert.Equal(t, http.StatusNotFound, r.status, path)
		assert.Equal(t, httpsse.ReasonQueryDisabled, r.errorBody(t).Reason, path)
	}

	var c transport.Capabilities
	require.NoError(t, json.Unmarshal(get(t, ts.URL+httpsse.BasePath).body, &c))
	assert.Equal(t, transport.Operations{Watch: true}, c.Operations)
}

func TestQueryCapabilities(t *testing.T) {
	ts := startHTTP(t, newQueryStore(t), httpsse.Options{QueryEnabled: true})
	var c transport.Capabilities
	require.NoError(t, json.Unmarshal(get(t, ts.URL+httpsse.BasePath).body, &c))
	assert.Equal(t, transport.Operations{Watch: true, List: true, Get: true}, c.Operations)
}

func TestListNodes(t *testing.T) {
	ts := startHTTP(t, newQueryStore(t), httpsse.Options{QueryEnabled: true})

	cursor, keys := get(t, ts.URL+httpsse.NodesPath).list(t)
	assert.Equal(t, httpsse.Cursor{Epoch: epoch, Revision: 4}, cursor)
	assert.Equal(t, []string{cloudKey, edgeKey}, keys)

	_, keys = get(t, ts.URL+httpsse.NodesPath+"?category=edge-worker").list(t)
	assert.Equal(t, []string{edgeKey}, keys)

	_, keys = get(t, ts.URL+httpsse.NodesPath+"?category=cloud-worker").list(t)
	assert.Empty(t, keys, "an empty list, not an error")

	r := get(t, ts.URL+httpsse.NodesPath+"?category=edge")
	assert.Equal(t, http.StatusBadRequest, r.status)
	assert.Equal(t, httpsse.ReasonBadRequest, r.errorBody(t).Reason)
	assert.Contains(t, r.errorBody(t).Error, "edge-worker")
}

func TestListDevices(t *testing.T) {
	ts := startHTTP(t, newQueryStore(t), httpsse.Options{QueryEnabled: true})

	_, keys := get(t, ts.URL+httpsse.DevicesPath).list(t)
	assert.Equal(t, []string{deviceKey, thermalKey}, keys)

	_, keys = get(t, ts.URL+httpsse.DevicesPath+"?node=u2deb-edge").list(t)
	assert.Equal(t, []string{deviceKey}, keys)

	_, keys = get(t, ts.URL+httpsse.DevicesPath+"?namespace=vehicle-a").list(t)
	assert.Equal(t, []string{thermalKey}, keys)

	_, keys = get(t, ts.URL+httpsse.DevicesPath+"?namespace=vehicle-a&node=u2deb-edge").list(t)
	assert.Empty(t, keys)
}

func TestGetNode(t *testing.T) {
	ts := startHTTP(t, newQueryStore(t), httpsse.Options{QueryEnabled: true})

	r := get(t, ts.URL+httpsse.NodesPath+"/u2deb-edge")
	require.Equal(t, http.StatusOK, r.status)
	var res struct {
		Key  string         `json:"key"`
		Spec model.NodeSpec `json:"spec"`
	}
	require.NoError(t, json.Unmarshal(r.body, &res))
	assert.Equal(t, edgeKey, res.Key)
	assert.Equal(t, model.NodeCategoryEdgeWorker, res.Spec.Category)

	r = get(t, ts.URL+httpsse.NodesPath+"/u2deb-cloud")
	require.Equal(t, http.StatusOK, r.status)

	r = get(t, ts.URL+httpsse.NodesPath+"/missing")
	assert.Equal(t, http.StatusNotFound, r.status)
	assert.Equal(t, httpsse.ReasonNotFound, r.errorBody(t).Reason)
}

func TestGetDevice(t *testing.T) {
	st := newQueryStore(t)
	// Give the tire device a second property.
	r := tire("230.0")
	r.Spec.(*model.DeviceSpec).Properties = append(r.Spec.(*model.DeviceSpec).Properties,
		model.DeviceProperty{Name: "lowPressureWarning", Type: "BOOLEAN", Reported: &model.TwinValue{Value: "false"}})
	mustUpsert(t, st, r)
	ts := startHTTP(t, st, httpsse.Options{QueryEnabled: true})
	path := ts.URL + httpsse.DevicesPath + "/default/automotive-tire-pressure-ecu-fw-001"

	properties := func(resp response) []string {
		t.Helper()
		require.Equal(t, http.StatusOK, resp.status, string(resp.body))
		var res struct {
			Spec model.DeviceSpec `json:"spec"`
		}
		require.NoError(t, json.Unmarshal(resp.body, &res))
		names := []string{}
		for _, p := range res.Spec.Properties {
			names = append(names, p.Name)
		}
		return names
	}

	assert.Equal(t, []string{"frontLeftPressure", "lowPressureWarning"}, properties(get(t, path)))
	assert.Equal(t, []string{"lowPressureWarning"}, properties(get(t, path+"?property=lowPressureWarning")))
	assert.Equal(t, []string{"frontLeftPressure", "lowPressureWarning"},
		properties(get(t, path+"?property=lowPressureWarning&property=frontLeftPressure&property=lowPressureWarning")), "spec order, no duplicates")

	missing := get(t, path+"?property=frontLeftPressure&property=rearLeftPressure&property=rearLeftPressure")
	assert.Equal(t, http.StatusNotFound, missing.status)
	assert.Equal(t, httpsse.ReasonNotFound, missing.errorBody(t).Reason)
	assert.Contains(t, missing.errorBody(t).Error, "has no property rearLeftPressure")
	assert.NotContains(t, missing.errorBody(t).Error, "rearLeftPressure, rearLeftPressure")

	// The filter did not change the published state.
	assert.Equal(t, []string{"frontLeftPressure", "lowPressureWarning"}, properties(get(t, path)))
	stored, _ := st.Get(deviceKey)
	assert.Len(t, stored.Spec.(*model.DeviceSpec).Properties, 2)

	nf := get(t, ts.URL+httpsse.DevicesPath+"/default/missing")
	assert.Equal(t, http.StatusNotFound, nf.status)
	assert.Equal(t, httpsse.ReasonNotFound, nf.errorBody(t).Reason)
}

// TestListThenWatch checks that a list cursor resumes a watch without a gap or a repeat.
func TestListThenWatch(t *testing.T) {
	st := newQueryStore(t)
	ts := startHTTP(t, st, httpsse.Options{QueryEnabled: true})
	cursor, _ := get(t, ts.URL+httpsse.DevicesPath).list(t)

	mustUpsert(t, st, tire("225.0")) // 5, after the list
	s := watch(t, ts.URL, "prefix=devices/&since=4&epoch="+cursor.Epoch, nil)
	assert.Equal(t, []summary{
		{Event: httpsse.EventUpsert, Key: deviceKey},
		{Event: httpsse.EventSynced, ID: epoch + ":5"},
	}, s.summarize(t, 2))
}
