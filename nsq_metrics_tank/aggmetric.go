package main

import (
	"errors"
	"fmt"
	"sync"

	"github.com/dgryski/go-tsz"
)

// while AggMetric typically has 1 static chunkSpan (we decide what is the preferred way to encode the data),
// it must support different aggregator spans. in particular:
// 1. aggregator spans may be shorter (e.g. chunkSpan is 4hours but we collect aggregates on 5/10/30 minute level)
// 2. aggregator spans may be larger (e.g. chunkSpan is 2 hours but we decide to collect daily aggregates)
// we want to be able to keep multiple chunks per AggMetric, to satisfy (2) but also because it makes sense to be able
// to serve the data from RAM if we have RAM available.
// so, numChunks must at least cover the highest aggregation interval

// responsible for taking in new values, updating the in-memory data
// and informing aggregators when their periods have lapsed
type AggMetric struct {
	sync.Mutex
	key         string
	lastTs      uint32 // last timestamp seen
	firstStart  uint32 // rounded to chunkSpan. denotes the very first start seen, ever, even if no longer in range
	lastStart   uint32 // rounded to chunkSpan. denotes last start seen.
	numChunks   uint32 // amount of chunks to keep
	chunkSpan   uint32 // span of individual chunks in seconds
	chunks      []*tsz.Series
	aggregators []*Aggregator
}

//first Start 100 at 0
//(lastStart - firstStart) / span % 4
//100 - 100 -> 0
//200 - 100 -> 1
//300 - 100 -> 2
//400 - 100 -> 3
//500 - 100 -> 0
//600 - 100 -> 1
//700 - 100 -> 2
//800 - 100 -> 3
// start pos 100, 200, 300, 400 - lastTs 425 -> 400 : 100 200 300 400
// start pos 200, 300, 400, 500 - lastTs 585 -> 500 : 500 200 300 400 / 300-400 -> 2:3 / 300-500 -> 2:0
// start pos 500, 600, 700, 800 - lastTs 885 -> 800 : 500 600 700 800 / 500-700 -> 0:2
// 1:0 -> 4
// 2:0 -> 3
// 3:0 -> 2

//(4 - start) + end + 1

// using user input, which has to be verified, and could be any input
func (a *AggMetric) GetUnsafe(from, to uint32) ([]*tsz.Iter, error) {
	if from >= to {
		return nil, errors.New("invalid request. to must > from")
	}
	a.Lock()
	firstStart := from - (from % a.chunkSpan)
	lastStart := (to - 1) - ((to - 1) % a.chunkSpan) // TODO verify /rethink/simplify this
	if firstStart < a.lastStart-a.numChunks*a.chunkSpan {
		firstStart = a.lastStart - a.numChunks*a.chunkSpan
	}
	if lastStart > a.lastStart {
		lastStart = a.lastStart
	}

	defer a.Unlock()
	return a.get(firstStart, lastStart), nil
}

// using input from our software, which should already be solid.
// returns a range that includes the requested range, but typically more.
func (a *AggMetric) GetSafe(from, to uint32) []*tsz.Iter {
	if from >= to {
		panic("invalid request. to must > from")
	}
	a.Lock()
	firstStart := from - (from % a.chunkSpan)
	lastStart := (to - 1) - ((to - 1) % a.chunkSpan) // TODO verify /rethink/simplify this
	if firstStart < a.lastStart-a.numChunks*a.chunkSpan {
		panic("requested a firstStart that is too old")
	}
	if lastStart > a.lastStart {
		panic("requested a lastStart that doesn't exist yet")
	}
	defer a.Unlock()
	return a.get(firstStart, lastStart)
}

// like slices,
// from inclusive
// to exclusive
// firstStart and lastStart must be aligned to marker intervals
func (a *AggMetric) get(firstStart, lastStart uint32) []*tsz.Iter {
	first := ((firstStart - a.firstStart) / a.chunkSpan) % a.numChunks
	last := ((lastStart - a.firstStart) / a.chunkSpan) % a.numChunks
	var data []*tsz.Series
	if last >= first {
		data = a.chunks[first : last+1]
	} else {
		//     the values at the end + values at the beginning
		num := (a.numChunks - first) + last + 1
		data = make([]*tsz.Series, 0, num)
		// at the values at the end of chunks slice first (they are first in time)
		for i := first; i < a.numChunks; i++ {
			data = append(data, a.chunks[i])
		}
		// then the values later in time, which are at the beginning of the slice
		for i := uint32(0); i <= last; i++ {
			data = append(data, a.chunks[i])
		}
	}
	iters := make([]*tsz.Iter, 0, len(data))
	for _, chunk := range data {
		if chunk != nil {
			iters = append(iters, chunk.Iter())
		}
	}
	return iters
}

func NewAggMetric(key string) *AggMetric {
	numChunks := uint32(5)
	m := AggMetric{
		key: key,
		//		chunkSpan:   60 * 60 * 2, // 2 hours
		chunkSpan: 60 * 2,
		numChunks: numChunks,
		chunks:    make([]*tsz.Series, numChunks),
	}
	return &m
}

// this function must only be called while holding the lock
func (a *AggMetric) signalAggregators(ts uint32) {
	for _, agg := range a.aggregators {
		agg.Signal(a, ts)
	}
}

func (a *AggMetric) Add(ts uint32, val float64) {
	a.Lock()
	defer a.Unlock()
	fmt.Println(a.key, "adding", val, ts)
	if ts <= a.lastTs {
		fmt.Println("ERROR: ts <= last seen ts")
		return
	}
	start := ts - (ts % a.chunkSpan)

	// if we're adding first point ever..
	if a.firstStart == 0 {
		a.firstStart = start
		a.lastStart = start
		series := tsz.New(start)
		series.Push(ts, val)
		a.chunks[0] = series
		a.signalAggregators(ts)
		return
	}

	if start == a.lastStart {
		// last prior data was in same chunk as new point
		index := ((start - a.firstStart) / a.chunkSpan) % a.numChunks
		a.chunks[index].Push(ts, val)
	} else {
		// the point needs a newer chunk than points we've seen before
		// start is higher than lastStart, because we already checked the ts

		// finish last chunk
		lastIndex := ((a.lastStart - a.firstStart) / a.chunkSpan) % a.numChunks
		a.chunks[lastIndex].Finish()

		// create new chunk
		series := tsz.New(start)
		series.Push(ts, val)
		newIndex := ((start - a.firstStart) / a.chunkSpan) % a.numChunks
		a.chunks[newIndex] = series

		// create empty series in between, if there's a gap. TODO
	}
	a.signalAggregators(ts)
}
