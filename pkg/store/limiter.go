// Copyright (c) The Thanos Authors.
// Licensed under the Apache License 2.0.

package store

import (
	"runtime"
	"runtime/metrics"
	"sync"
	"time"

	"github.com/KimMachineGun/automemlimit/memlimit"
	"github.com/alecthomas/units"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/atomic"

	"github.com/thanos-io/thanos/pkg/extkingpin"
	"github.com/thanos-io/thanos/pkg/store/storepb"
)

type ChunksLimiter interface {
	// Reserve num chunks out of the total number of chunks enforced by the limiter.
	// Returns an error if the limit has been exceeded. This function must be
	// goroutine safe.
	Reserve(num uint64) error
}

type SeriesLimiter interface {
	// Reserve num series out of the total number of series enforced by the limiter.
	// Returns an error if the limit has been exceeded. This function must be
	// goroutine safe.
	Reserve(num uint64) error
}

type BytesLimiter interface {
	// Reserve bytes out of the total amount of bytes enforced by the limiter.
	// Returns an error if the limit has been exceeded. This function must be
	// goroutine safe.
	ReserveWithType(num uint64, dataType StoreDataType) error
}

// ChunksLimiterFactory is used to create a new ChunksLimiter. The factory is useful for
// projects depending on Thanos (eg. Cortex) which have dynamic limits.
type ChunksLimiterFactory func(failedCounter prometheus.Counter) ChunksLimiter

// SeriesLimiterFactory is used to create a new SeriesLimiter.
type SeriesLimiterFactory func(failedCounter prometheus.Counter) SeriesLimiter

// BytesLimiterFactory is used to create a new BytesLimiter.
type BytesLimiterFactory func(failedCounter prometheus.Counter) BytesLimiter

// Limiter is a simple mechanism for checking if something has passed a certain threshold.
type Limiter struct {
	limit    uint64
	reserved atomic.Uint64

	// Counter metric which we will increase if limit is exceeded.
	failedCounter prometheus.Counter
	failedOnce    sync.Once
}

// NewLimiter returns a new limiter with a specified limit. 0 disables the limit.
func NewLimiter(limit uint64, ctr prometheus.Counter) *Limiter {
	return &Limiter{limit: limit, failedCounter: ctr}
}

// Reserve implements ChunksLimiter.
func (l *Limiter) Reserve(num uint64) error {
	return l.ReserveWithType(num, 0)
}

func (l *Limiter) ReserveWithType(num uint64, _ StoreDataType) error {
	if l == nil {
		return nil
	}
	if l.limit == 0 {
		return nil
	}
	if reserved := l.reserved.Add(num); reserved > l.limit {
		// We need to protect from the counter being incremented twice due to concurrency
		// while calling Reserve().
		l.failedOnce.Do(l.failedCounter.Inc)
		return errors.Errorf("limit %v violated (got %v)", l.limit, reserved)
	}
	return nil
}

// NewChunksLimiterFactory makes a new ChunksLimiterFactory with a static limit.
func NewChunksLimiterFactory(limit uint64) ChunksLimiterFactory {
	return func(failedCounter prometheus.Counter) ChunksLimiter {
		return NewLimiter(limit, failedCounter)
	}
}

// NewSeriesLimiterFactory makes a new SeriesLimiterFactory with a static limit.
func NewSeriesLimiterFactory(limit uint64) SeriesLimiterFactory {
	return func(failedCounter prometheus.Counter) SeriesLimiter {
		return NewLimiter(limit, failedCounter)
	}
}

// NewBytesLimiterFactory makes a new BytesLimiterFactory with a static limit.
func NewBytesLimiterFactory(limit units.Base2Bytes) BytesLimiterFactory {
	return func(failedCounter prometheus.Counter) BytesLimiter {
		return NewLimiter(uint64(limit), failedCounter)
	}
}

// SeriesSelectLimits are limits applied against individual Series calls.
type SeriesSelectLimits struct {
	SeriesPerRequest  uint64
	SamplesPerRequest uint64
	MemoryRatio       float64
}

func (l *SeriesSelectLimits) RegisterFlags(cmd extkingpin.FlagClause) {
	cmd.Flag("store.limits.request-series", "The maximum series allowed for a single Series request. The Series call fails if this limit is exceeded. 0 means no limit.").Default("0").Uint64Var(&l.SeriesPerRequest)
	cmd.Flag("store.limits.request-samples", "The maximum samples allowed for a single Series request, The Series call fails if this limit is exceeded. 0 means no limit. NOTE: For efficiency the limit is internally implemented as 'chunks limit' considering each chunk contains a maximum of 120 samples.").Default("0").Uint64Var(&l.SamplesPerRequest)
}

// RegisterMemoryFlag registers the memory limit, applied by NewLimitedStoreServer.
func (l *SeriesSelectLimits) RegisterMemoryFlag(cmd extkingpin.FlagClause) {
	cmd.Flag("store.limits.memory-ratio", "Series calls fail once memory used by the process exceeds this ratio of the detected container (cgroup) or system memory. 0 means no limit.").Default("0").FloatVar(&l.MemoryRatio)
}

var _ storepb.StoreServer = &limitedStoreServer{}

// limitedStoreServer is a storepb.StoreServer that can apply series and sample limits against individual Series requests.
type limitedStoreServer struct {
	storepb.StoreServer
	newSeriesLimiter      SeriesLimiterFactory
	newSamplesLimiter     ChunksLimiterFactory
	memoryBudget          *memoryBudget
	failedRequestsCounter *prometheus.CounterVec
}

// memoryLimit detects the memory available to the process.
var memoryLimit = memlimit.ApplyFallback(memlimit.FromCgroup, memlimit.FromSystem)

// NewLimitedStoreServer creates a new limitedStoreServer.
func NewLimitedStoreServer(store storepb.StoreServer, reg prometheus.Registerer, selectLimits SeriesSelectLimits) storepb.StoreServer {
	s := &limitedStoreServer{
		StoreServer:       store,
		newSeriesLimiter:  NewSeriesLimiterFactory(selectLimits.SeriesPerRequest),
		newSamplesLimiter: NewChunksLimiterFactory(selectLimits.SamplesPerRequest),
		failedRequestsCounter: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "thanos_store_selects_dropped_total",
			Help: "Number of select queries that were dropped due to configured limits.",
		}, []string{"reason"}),
	}
	budget := promauto.With(reg).NewGauge(prometheus.GaugeOpts{
		Name: "thanos_store_limits_memory_budget_bytes",
		Help: "Memory used by the process after which Series calls are rejected, 0 if disabled.",
	})
	if selectLimits.MemoryRatio > 0 {
		if limit, err := memoryLimit(); err == nil {
			s.memoryBudget = &memoryBudget{limit: uint64(float64(limit) * selectLimits.MemoryRatio)}
			budget.Set(float64(s.memoryBudget.limit))
		}
	}
	return s
}

func (s *limitedStoreServer) Series(req *storepb.SeriesRequest, srv storepb.Store_SeriesServer) error {
	seriesLimiter := s.newSeriesLimiter(s.failedRequestsCounter.WithLabelValues("series"))
	chunksLimiter := s.newSamplesLimiter(s.failedRequestsCounter.WithLabelValues("chunks"))
	limitedSrv := newLimitedServer(srv, seriesLimiter, chunksLimiter)
	limitedSrv.memoryBudget = s.memoryBudget
	limitedSrv.memoryFailures = s.failedRequestsCounter.WithLabelValues("memory")
	if err := s.StoreServer.Series(req, limitedSrv); err != nil {
		return err
	}

	return nil
}

var _ storepb.Store_SeriesServer = &limitedServer{}

// limitedServer is a storepb.Store_SeriesServer that tracks statistics about sent series.
type limitedServer struct {
	storepb.Store_SeriesServer
	seriesLimiter  SeriesLimiter
	samplesLimiter ChunksLimiter
	memoryBudget   *memoryBudget
	memoryFailures prometheus.Counter
	// reserved is set once series were reserved while buffered, so they are not counted again on Send.
	reserved bool
}

func newLimitedServer(upstream storepb.Store_SeriesServer, seriesLimiter SeriesLimiter, chunksLimiter ChunksLimiter) *limitedServer {
	return &limitedServer{
		Store_SeriesServer: upstream,
		seriesLimiter:      seriesLimiter,
		samplesLimiter:     chunksLimiter,
	}
}

func (i *limitedServer) Send(response *storepb.SeriesResponse) error {
	if i.reserved {
		return i.Store_SeriesServer.Send(response)
	}

	var seriesCount, chunksCount uint64
	if series := response.GetSeries(); series != nil {
		seriesCount = 1
		chunksCount = uint64(len(series.Chunks))
	} else if batch := response.GetBatch(); batch != nil {
		for _, series := range batch.Series {
			if series == nil {
				continue
			}
			seriesCount++
			chunksCount += uint64(len(series.Chunks))
		}
	} else {
		return i.Store_SeriesServer.Send(response)
	}

	if err := i.reserve(seriesCount, chunksCount); err != nil {
		return err
	}
	return i.Store_SeriesServer.Send(response)
}

func (i *limitedServer) reserveBuffered(series *storepb.Series) error {
	i.reserved = true
	return i.reserve(1, uint64(len(series.Chunks)))
}

func (i *limitedServer) reserve(seriesCount, chunksCount uint64) error {
	if err := i.memoryBudget.check(); err != nil {
		i.memoryFailures.Inc()
		return errors.Wrapf(err, "failed to send series")
	}
	if err := i.seriesLimiter.Reserve(seriesCount); err != nil {
		return errors.Wrapf(err, "failed to send series")
	}
	if err := i.samplesLimiter.Reserve(chunksCount * MaxSamplesPerChunk); err != nil {
		return errors.Wrapf(err, "failed to send samples")
	}
	return nil
}

// bufferedSeriesReserver is implemented by servers enforcing limits and by wrappers forwarding to them.
// Servers buffering series before sending (see resortingServer) use it to enforce limits
// before series are held in memory, instead of only when they are sent upstream.
type bufferedSeriesReserver interface {
	reserveBuffered(series *storepb.Series) error
}

func reserveBuffered(upstream storepb.Store_SeriesServer, series *storepb.Series) error {
	if r, ok := upstream.(bufferedSeriesReserver); ok {
		return r.reserveBuffered(series)
	}
	return nil
}

const (
	// memoryCheckInterval limits how often memory usage is read, keeping reservations cheap.
	memoryCheckInterval = 10 * time.Millisecond
	// memoryGCInterval limits how often garbage is collected to tell it apart from live memory.
	memoryGCInterval = time.Second
)

// memoryBudget rejects reservations once memory used by the process exceeds the limit.
type memoryBudget struct {
	limit     uint64
	lastCheck atomic.Int64
	lastGC    atomic.Int64
	exceeded  atomic.Bool
}

func (b *memoryBudget) check() error {
	if b == nil {
		return nil
	}
	now := time.Now().UnixNano()
	if last := b.lastCheck.Load(); now-last >= int64(memoryCheckInterval) && b.lastCheck.CompareAndSwap(last, now) {
		exceeded := memoryInUse() > b.limit
		if exceeded && now-b.lastGC.Load() >= int64(memoryGCInterval) {
			b.lastGC.Store(now)
			runtime.GC()
			exceeded = memoryInUse() > b.limit
		}
		b.exceeded.Store(exceeded)
	}
	if b.exceeded.Load() {
		return errors.Errorf("memory budget of %d bytes exceeded", b.limit)
	}
	return nil
}

// memoryInUse returns memory mapped by the Go runtime, excluding free heap retained or released to the OS.
func memoryInUse() uint64 {
	s := []metrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/free:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
	}
	metrics.Read(s)
	return s[0].Value.Uint64() - s[1].Value.Uint64() - s[2].Value.Uint64()
}
