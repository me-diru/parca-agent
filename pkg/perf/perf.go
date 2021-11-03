// Copyright 2021 The Parca Authors
//
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

package perf

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/go-kit/log"

	"github.com/parca-dev/parca-agent/pkg/hash"
)

type PerfCache struct {
	fs         fs.FS
	logger     log.Logger
	cache      map[uint32]*PerfMap
	pidMapHash map[uint32]uint64
}

type PerfMapAddr struct {
	Start  uint64
	End    uint64
	Symbol string
}

type PerfMap struct {
	addrs []PerfMapAddr
}

type realfs struct{}

var (
	NoSymbolFound = errors.New("no symbol found")
)

func (f *realfs) Open(name string) (fs.File, error) {
	return os.Open(name)
}

func PerfReadMap(fs fs.FS, fileName string) (PerfMap, error) {
	fd, err := fs.Open(fileName)
	if err != nil {
		return PerfMap{}, err
	}
	defer fd.Close()

	s := bufio.NewScanner(fd)
	addrs := make([]PerfMapAddr, 0)
	for s.Scan() {
		l := strings.SplitN(s.Text(), " ", 3)
		if len(l) < 3 {
			return PerfMap{}, fmt.Errorf("splitting failed: %v", l)

		}

		start, err := strconv.ParseUint(l[0], 16, 64)
		if err != nil {
			return PerfMap{}, fmt.Errorf("parsing start failed on %v: %w", l, err)
		}
		size, err := strconv.ParseUint(l[1], 16, 64)
		if err != nil {
			return PerfMap{}, fmt.Errorf("parsing end failed on %v: %w", l, err)
		}
		if start+size < start {
			return PerfMap{}, fmt.Errorf("overflowed mapping: %v", l)
		}
		addrs = append(addrs, PerfMapAddr{start, start + size, l[2]})
	}
	// Sorted by end address to allow binary search during look-up. End to find
	// the (closest) address _before_ the end. This could be an inlined instruction
	// within a larger blob.
	sort.Slice(addrs, func(i, j int) bool {
		return addrs[i].End < addrs[j].End
	})
	return PerfMap{addrs: addrs}, s.Err()
}

func (p *PerfMap) Lookup(addr uint64) (string, error) {
	idx := sort.Search(len(p.addrs), func(i int) bool {
		return addr < p.addrs[i].End
	})
	if idx == len(p.addrs) || p.addrs[idx].Start > addr {
		return "", NoSymbolFound
	}

	return p.addrs[idx].Symbol, nil
}

func NewPerfCache(logger log.Logger) *PerfCache {
	return &PerfCache{
		fs:         &realfs{},
		logger:     logger,
		cache:      map[uint32]*PerfMap{},
		pidMapHash: map[uint32]uint64{},
	}
}

// CacheForPid returns the PerfMap for the given pid if it exists.
func (p *PerfCache) CacheForPid(pid uint32) (*PerfMap, error) {
	// NOTE(zecke): There are various limitations and things to note.
	// 1st) The input file is "tainted" and under control by the user. By all
	//      means it could be an infinitely large.
	// 2nd) There might be a file called /tmp/perf-${nspid}.txt but that might
	//      be in a different mount_namespace(7) and pid_namespace(7). We don't
	//      map these yet. Using /proc/$pid/tmp/perf-$pid.txt is not enough and
	//      hence containerized workloads are broken.

	perfFile := fmt.Sprintf("/tmp/perf-%d.map", pid)
	// TODO(zecke): Log other than file not found errors?
	h, err := hash.File(p.fs, perfFile)
	if err != nil {
		return nil, err
	}

	if p.pidMapHash[pid] == h {
		return p.cache[pid], nil
	}

	m, err := PerfReadMap(p.fs, perfFile)
	if err != nil {
		return nil, err
	}

	p.cache[pid] = &m
	p.pidMapHash[pid] = h // TODO(zecke): Resolve time of check/time of use.
	return &m, nil
}