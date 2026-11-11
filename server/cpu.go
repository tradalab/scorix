package server

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	bucketWidth = 100 * time.Millisecond
	bucketCount = 50
	coolOff     = time.Second
	sampleEvery = 250 * time.Millisecond
	cpuDecay    = 0.95
	clockTicks  = 100
)

type cpuSource struct {
	used  func() (time.Duration, error)
	cores float64
}

func ownCgroup(root fs.FS) (string, bool) {
	b, err := fs.ReadFile(root, "proc/self/cgroup")
	if err != nil {
		return "", false
	}
	for line := range strings.Lines(string(b)) {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok {
			return path.Join("sys/fs/cgroup", p), true
		}
	}
	return "", false
}

func quota(root fs.FS, dir string) float64 {
	best := math.Inf(1)
	for {
		if b, err := fs.ReadFile(root, path.Join(dir, "cpu.max")); err == nil {
			f := strings.Fields(string(b))
			if len(f) == 2 && f[0] != "max" {
				q, qerr := strconv.ParseFloat(f[0], 64)
				p, perr := strconv.ParseFloat(f[1], 64)
				if qerr == nil && perr == nil && p > 0 {
					best = min(best, q/p)
				}
			}
		}
		if dir == "sys/fs/cgroup" || dir == "." || dir == "/" {
			return best
		}
		dir = path.Dir(dir)
	}
}

func cpuOf(root fs.FS, procs int) (cpuSource, error) {
	if dir, ok := ownCgroup(root); ok {
		if _, err := fs.Stat(root, path.Join(dir, "cpu.stat")); err == nil {
			cores := min(quota(root, dir), float64(procs))
			return cpuSource{cores: cores, used: func() (time.Duration, error) {
				b, err := fs.ReadFile(root, path.Join(dir, "cpu.stat"))
				if err != nil {
					return 0, err
				}
				sc := bufio.NewScanner(bytes.NewReader(b))
				for sc.Scan() {
					if v, ok := strings.CutPrefix(sc.Text(), "usage_usec "); ok {
						us, err := strconv.ParseInt(v, 10, 64)
						return time.Duration(us) * time.Microsecond, err
					}
				}
				return 0, errors.New("cpu.stat has no usage_usec")
			}}, nil
		}
	}
	if _, err := fs.Stat(root, "proc/self/stat"); err != nil {
		return cpuSource{}, errors.New("neither a cgroup v2 cpu.stat nor /proc/self/stat is readable")
	}
	return cpuSource{cores: float64(procs), used: func() (time.Duration, error) {
		b, err := fs.ReadFile(root, "proc/self/stat")
		if err != nil {
			return 0, err
		}
		s := string(b)
		f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
		if len(f) < 13 {
			return 0, fmt.Errorf("/proc/self/stat has %d fields after the name", len(f))
		}
		ut, uerr := strconv.ParseInt(f[11], 10, 64)
		st, serr := strconv.ParseInt(f[12], 10, 64)
		if uerr != nil || serr != nil {
			return 0, errors.New("/proc/self/stat utime or stime is not a number")
		}
		return time.Duration(ut+st) * time.Second / clockTicks, nil
	}}, nil
}

type sampler struct {
	src   cpuSource
	share atomic.Uint64
	last  time.Duration
	at    time.Time
}

func (s *sampler) load() float64 { return math.Float64frombits(s.share.Load()) }

func (s *sampler) sample(now time.Time) error {
	used, err := s.src.used()
	if err != nil {
		return err
	}
	if now.After(s.at) {
		cur := float64(used-s.last) / float64(now.Sub(s.at)) / s.src.cores
		s.share.Store(math.Float64bits(s.load()*cpuDecay + cur*(1-cpuDecay)))
	}
	s.last, s.at = used, now
	return nil
}

type bucket struct {
	index int64
	pass  int64
	rt    time.Duration
}

type shedder struct {
	threshold float64
	cpu       func() float64
	now       func() time.Time
	inflight  atomic.Int64
	mu        sync.Mutex
	buckets   [bucketCount]bucket
	dropped   time.Time
}

func (s *shedder) capacity(now time.Time) (float64, bool) {
	cur := now.UnixNano() / int64(bucketWidth)
	var maxPass int64
	minRT := time.Duration(math.MaxInt64)
	for _, b := range s.buckets {
		if b.index <= cur-bucketCount || b.index >= cur || b.pass == 0 {
			continue
		}
		maxPass = max(maxPass, b.pass)
		minRT = min(minRT, b.rt/time.Duration(b.pass))
	}
	if maxPass == 0 {
		return 0, false
	}
	return float64(maxPass) * float64(time.Second/bucketWidth) * minRT.Seconds(), true
}

func (s *shedder) admit(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	hot := s.cpu() > s.threshold || (!s.dropped.IsZero() && now.Sub(s.dropped) < coolOff)
	if hot {
		if limit, known := s.capacity(now); known && float64(s.inflight.Load()) >= max(limit, 1) {
			s.dropped = now
			return false
		}
	}
	return true
}

func (s *shedder) allow() (func(), bool) {
	now := s.now()
	if !s.admit(now) {
		return nil, false
	}
	s.inflight.Add(1)
	return func() {
		end := s.now()
		s.inflight.Add(-1)
		idx := end.UnixNano() / int64(bucketWidth)
		s.mu.Lock()
		b := &s.buckets[idx%bucketCount]
		if b.index != idx {
			*b = bucket{index: idx}
		}
		b.pass++
		b.rt += end.Sub(now)
		s.mu.Unlock()
	}, true
}
