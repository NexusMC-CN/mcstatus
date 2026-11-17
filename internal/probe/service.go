package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/mcstatus-io/mcutil/v4/response"
)

var ErrQueueFull = errors.New("probe queue is full")
var ErrClosed = errors.New("probe service is closed")

type queuedJob struct {
	id string
}

type Service struct {
	io           networkIO
	slots        chan struct{}
	lifetime     context.Context
	cancel       context.CancelFunc
	active       sync.WaitGroup
	closed       bool
	startOnce    sync.Once
	closeOnce    sync.Once
	workers      int
	timeout      time.Duration
	jobTTL       time.Duration
	queue        chan queuedJob
	jobs         map[string]*Job
	mu           sync.RWMutex
	stopCh       chan struct{}
	wg           sync.WaitGroup
	cleanupGroup sync.WaitGroup
}

func NewService(workers int, queueSize int, timeout time.Duration, jobTTL time.Duration) *Service {
	if 1 > workers {
		workers = 1
	}
	if 1 > queueSize {
		queueSize = 1
	}
	if 0 >= timeout {
		timeout = 4 * time.Second
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &Service{
		workers:  workers,
		timeout:  timeout,
		jobTTL:   jobTTL,
		queue:    make(chan queuedJob, queueSize),
		jobs:     make(map[string]*Job),
		stopCh:   make(chan struct{}),
		io:       defaultNetworkIO(),
		slots:    make(chan struct{}, workers),
		lifetime: lifetime,
		cancel:   cancel,
	}
}

func (s *Service) Start() {
	s.startOnce.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			return
		}
		for i := 0; i < s.workers; i++ {
			s.wg.Add(1)
			go s.worker()
		}

		s.cleanupGroup.Add(1)
		go s.cleanupLoop()
	})
}

func (s *Service) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		close(s.stopCh)
		s.mu.Unlock()
		s.wg.Wait()
		s.cleanupGroup.Wait()
		s.active.Wait()
	})
}

func (s *Service) Probe(ctx context.Context, req Request) (*Result, error) {
	return s.runProbe(ctx, req)
}

func (s *Service) Submit(req Request) (*Job, error) {
	if err := validateRequest(normalizeRequest(req)); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	job := &Job{
		ID:        newJobID(),
		Status:    JobPending,
		Request:   cloneRequest(req),
		CreatedAt: now,
		ExpiresAt: now.Add(s.jobTTL),
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	s.jobs[job.ID] = job
	s.mu.Unlock()

	select {
	case s.queue <- queuedJob{id: job.ID}:
		s.mu.RLock()
		snapshot := cloneJob(job)
		s.mu.RUnlock()
		return snapshot, nil
	default:
		s.mu.Lock()
		delete(s.jobs, job.ID)
		s.mu.Unlock()
		return nil, ErrQueueFull
	}
}

func (s *Service) GetJob(id string) (*Job, bool) {
	s.mu.RLock()
	job, ok := s.jobs[id]
	snapshot := cloneJob(job)
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return snapshot, true
}

func (s *Service) worker() {
	defer s.wg.Done()

	for {
		select {
		case <-s.stopCh:
			return
		case queued := <-s.queue:
			s.executeJob(queued.id)
		}
	}
}

func (s *Service) executeJob(id string) {
	s.mu.Lock()
	job, ok := s.jobs[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	job.Status = JobRunning
	job.StartedAt = &now
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), s.effectiveTimeout(job.Request))
	defer cancel()

	result, err := s.runProbe(ctx, job.Request)

	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok = s.jobs[id]
	if !ok {
		return
	}

	endedAt := time.Now().UTC()
	job.EndedAt = &endedAt

	if err != nil {
		message := err.Error()
		job.Status = JobFailed
		job.Error = &message
		return
	}

	job.Status = JobCompleted
	job.Result = result
	job.Error = nil
}

func (s *Service) runProbe(ctx context.Context, req Request) (*Result, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	s.active.Add(1)
	s.mu.Unlock()
	defer s.active.Done()
	ctx, cancel := context.WithTimeout(ctx, s.effectiveTimeout(req))
	defer cancel()
	stop := context.AfterFunc(s.lifetime, cancel)
	defer stop()
	req = normalizeRequest(req)
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return buildErrorResult(req, classifyProbeError(ctx.Err())), nil
	}
	if err := ctx.Err(); err != nil {
		return buildErrorResult(req, classifyProbeError(err)), nil
	}
	switch req.Platform {
	case PlatformBedrock:
		return s.probeBedrockTransport(ctx, req)
	default:
		return s.probeJavaPinned(ctx, req)
	}
}

func (s *Service) effectiveTimeout(req Request) time.Duration {
	if req.TimeoutMS > 0 {
		timeout := time.Duration(req.TimeoutMS) * time.Millisecond
		if timeout >= time.Second && timeout <= 30*time.Second {
			return timeout
		}
	}
	return s.timeout
}

func (s *Service) cleanupLoop() {
	defer s.cleanupGroup.Done()

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.cleanupExpiredJobs()
		}
	}
}

func (s *Service) cleanupExpiredJobs() {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, job := range s.jobs {
		if now.After(job.ExpiresAt) {
			delete(s.jobs, id)
		}
	}
}

func buildJavaModernResult(req Request, data *response.StatusModern) *Result {
	result := &Result{
		Platform:     PlatformJava,
		State:        StateOnline,
		CheckedAt:    time.Now().UTC(),
		IsOnline:     true,
		Latency:      durationToMilliseconds(data.Latency),
		OnlineCount:  data.Players.Online,
		MaxPlayers:   data.Players.Max,
		Version:      stringPtr(data.Version.Name.Clean),
		Protocol:     int64Ptr(data.Version.Protocol),
		Motd:         stringPtr(data.MOTD.Raw),
		Favicon:      data.Favicon,
		SamplePlayer: collectModernPlayers(data.Players.Sample),
		Source:       "mcstatus",
	}
	applySRVRecord(result, req.Host, data.SRVRecord, req.Port)
	return result
}

func buildJavaLegacyResult(req Request, data *response.StatusLegacy) *Result {
	result := &Result{
		Platform:     PlatformJava,
		State:        StateOnline,
		CheckedAt:    time.Now().UTC(),
		IsOnline:     true,
		Latency:      nil,
		OnlineCount:  int64Ptr(data.Players.Online),
		MaxPlayers:   int64Ptr(data.Players.Max),
		Motd:         stringPtr(data.MOTD.Raw),
		SamplePlayer: []string{},
		Source:       "mcstatus",
	}
	if data.Version != nil {
		result.Version = stringPtr(data.Version.Name.Clean)
		result.Protocol = int64Ptr(data.Version.Protocol)
	}
	applySRVRecord(result, req.Host, data.SRVRecord, req.Port)
	return result
}

func buildBedrockResult(req Request, data *response.StatusBedrock) *Result {
	result := &Result{
		Platform:     PlatformBedrock,
		State:        StateOnline,
		CheckedAt:    time.Now().UTC(),
		IsOnline:     true,
		Latency:      nil,
		OnlineCount:  data.OnlinePlayers,
		MaxPlayers:   data.MaxPlayers,
		Version:      data.Version,
		Protocol:     data.ProtocolVersion,
		SamplePlayer: []string{},
		Source:       "mcstatus",
		GameMode:     data.Gamemode,
	}
	if data.MOTD != nil {
		result.Motd = stringPtr(data.MOTD.Raw)
	}
	if data.PortIPv4 != nil {
		port := int(*data.PortIPv4)
		result.ResolvedPort = &port
	}
	host := req.Host
	result.ResolvedHost = &host
	return result
}

func buildErrorResult(req Request, state State) *Result {
	message := "当前无法获取服务器状态，请稍后重试"
	result := &Result{
		Platform:     req.Platform,
		State:        state,
		CheckedAt:    time.Now().UTC(),
		IsOnline:     false,
		OnlineCount:  int64Ptr(0),
		SamplePlayer: []string{},
		Source:       "mcstatus",
		Error:        &message,
	}
	host := req.Host
	port := req.Port
	result.ResolvedHost = &host
	result.ResolvedPort = &port
	return result
}

func collectModernPlayers(players []response.SamplePlayer) []string {
	values := make([]string, 0, len(players))
	for _, player := range players {
		name := strings.TrimSpace(player.Name.Clean)
		if name != "" {
			values = append(values, name)
		}
		if len(values) >= 12 {
			break
		}
	}
	return values
}

func applySRVRecord(result *Result, fallbackHost string, srv *response.SRVRecord, fallbackPort int) {
	if result == nil {
		return
	}
	if srv == nil {
		host := fallbackHost
		result.ResolvedHost = &host
		port := fallbackPort
		result.ResolvedPort = &port
		return
	}
	host := srv.Host
	port := int(srv.Port)
	result.ResolvedHost = &host
	result.ResolvedPort = &port
}

func normalizeRequest(req Request) Request {
	req = cloneRequest(req)
	req.Platform = Platform(strings.ToLower(strings.TrimSpace(string(req.Platform))))
	req.Host = strings.TrimSpace(req.Host)
	if req.Platform == "" {
		req.Platform = PlatformJava
	}
	if req.Port == 0 {
		if req.Platform == PlatformBedrock {
			req.Port = 19132
		} else {
			req.Port = 25565
		}
	}
	return req
}

func validateRequest(req Request) error {
	if req.Platform != PlatformJava && req.Platform != PlatformBedrock {
		return fmt.Errorf("unsupported platform: %s", req.Platform)
	}
	if req.Platform == PlatformBedrock {
		return validateBedrockRequest(req)
	}
	if req.Host == "" {
		return errors.New("host is required")
	}
	if req.Port < 1 || req.Port > 65535 {
		return errors.New("port is invalid")
	}
	return nil
}

func validatePublicTarget(host string) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !publicIP(ip) {
			return errors.New("private or local network targets are not allowed")
		}
		return nil
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return errors.New("target DNS resolution failed")
	}
	if len(ips) == 0 {
		return errors.New("target DNS resolution returned no addresses")
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		if !publicIP(addr) {
			return errors.New("private or local network targets are not allowed")
		}
	}
	return nil
}

func classifyProbeError(errors ...error) State {
	for _, err := range errors {
		if err == nil {
			continue
		}
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "deadline exceeded") || strings.Contains(message, "timeout") {
			return StateTimeout
		}
	}
	for _, err := range errors {
		if err != nil {
			return StateError
		}
	}
	return StateOffline
}

func timeoutFromContext(ctx context.Context, fallback time.Duration) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return fallback
	}
	timeout := time.Until(deadline)
	if timeout <= 0 {
		return time.Second
	}
	return timeout
}

func durationToMilliseconds(value time.Duration) *int64 {
	if value <= 0 {
		return nil
	}
	ms := value.Milliseconds()
	return &ms
}

func int64Ptr(value int64) *int64 {
	return &value
}

func stringPtr(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func cloneJob(job *Job) *Job {
	if job == nil {
		return nil
	}
	copyJob := *job
	copyJob.Request = cloneRequest(job.Request)
	copyJob.Result = cloneResult(job.Result)
	copyJob.Error = clonePointer(job.Error)
	copyJob.StartedAt = clonePointer(job.StartedAt)
	copyJob.EndedAt = clonePointer(job.EndedAt)
	return &copyJob
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneResult(value *Result) *Result {
	if value == nil {
		return nil
	}
	copied := *value
	copied.Latency = clonePointer(value.Latency)
	copied.OnlineCount = clonePointer(value.OnlineCount)
	copied.MaxPlayers = clonePointer(value.MaxPlayers)
	copied.Protocol = clonePointer(value.Protocol)
	copied.Version = clonePointer(value.Version)
	copied.Motd = clonePointer(value.Motd)
	copied.Favicon = clonePointer(value.Favicon)
	copied.GameMode = clonePointer(value.GameMode)
	copied.MapName = clonePointer(value.MapName)
	copied.Error = clonePointer(value.Error)
	copied.ResolvedHost = clonePointer(value.ResolvedHost)
	copied.ResolvedPort = clonePointer(value.ResolvedPort)
	copied.SamplePlayer = append([]string{}, value.SamplePlayer...)
	return &copied
}
