// Copyright 2021 The Parca Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"context"

	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	profilestorepb "github.com/parca-dev/parca/gen/proto/go/parca/profilestore/v1alpha1"
	"google.golang.org/grpc"
)

type Batcher struct {
	series      []*profilestorepb.RawProfileSeries
	writeClient profilestorepb.ProfileStoreServiceClient
	logger      log.Logger

	mtx                *sync.RWMutex
	lastBatchSentAt    time.Time
	lastBatchSendError error
}

func NewBatcher(wc profilestorepb.ProfileStoreServiceClient) *Batcher {
	return &Batcher{
		series:      []*profilestorepb.RawProfileSeries{},
		writeClient: wc,
		mtx:         &sync.RWMutex{},
	}
}

func (b *Batcher) loopReport(lastBatchSentAt time.Time, lastBatchSendError error) {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	b.lastBatchSentAt = lastBatchSentAt
	b.lastBatchSendError = lastBatchSendError
}

func (b *Batcher) Run(ctx context.Context) error {
	// TODO(Sylfrena): Make ticker duration configurable
	const tickerDuration = 10 * time.Second

	ticker := time.NewTicker(tickerDuration)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		err := b.batchLoop(ctx)

		b.loopReport(time.Now(), err)
	}
}

func (b *Batcher) batchLoop(ctx context.Context) error {
	b.mtx.Lock()
	defer b.mtx.Unlock()

	_, err := b.writeClient.WriteRaw(ctx,
		&profilestorepb.WriteRawRequest{Series: b.series})

	if err != nil {
		level.Error(b.logger).Log("msg", "Writeclient failed to send profiles", "err", err)
		return err
	}

	b.series = []*profilestorepb.RawProfileSeries{}

	return nil
}

func isEqualLabel(a *profilestorepb.LabelSet, b *profilestorepb.LabelSet) bool {
	ret := true

	if len(a.Labels) == len(b.Labels) {
		for i := range a.Labels {
			if (a.Labels[i].Name != b.Labels[i].Name) || (a.Labels[i].Value != b.Labels[i].Value) {
				ret = false
			}
		}
	} else {
		ret = false
	}

	return ret
}

func ifExists(arr []*profilestorepb.RawProfileSeries, p *profilestorepb.RawProfileSeries) (bool, int) {
	res := false

	for i, val := range arr {
		if isEqualLabel(val.Labels, p.Labels) {
			return true, i
		}
	}
	return res, -1
}

func (b *Batcher) WriteRaw(ctx context.Context, r *profilestorepb.WriteRawRequest, opts ...grpc.CallOption) (*profilestorepb.WriteRawResponse, error) {

	b.mtx.Lock()
	defer b.mtx.Unlock()

	for _, profileSeries := range r.Series {
		ok, j := ifExists(b.series, profileSeries)

		if ok {
			b.series[j].Samples = append(b.series[j].Samples, profileSeries.Samples...)
		} else {
			b.series = append(b.series, &profilestorepb.RawProfileSeries{
				Labels:  profileSeries.Labels,
				Samples: profileSeries.Samples,
			})
		}

	}

	return &profilestorepb.WriteRawResponse{}, nil

}