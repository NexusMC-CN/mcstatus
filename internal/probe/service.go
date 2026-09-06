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

	"github.com/mcstatus-io/mcutil/v4/options"
	"github.com/mcstatus-io/mcutil/v4/response"
	"github.com/mcstatus-io/mcutil/v4/status"
)

var ErrQueueFull = errors.New("probe queue is full")

type queuedJob struct {
	id string
}

type Service struct {
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
	return &Service{
		workers: workers,
		timeout: timeout,
		jobTTL:  jobTTL,
		queue:   make(chan queuedJob, queueSize),
		jobs:    make(map[string]*Job),
		stopCh:  make(chan struct{}),
	}
}

func (s *Service) Start() {
	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go s.worker()
	}

	s.cleanupGroup.Add(1)
	go s.cleanupLoop()
}

func (s *Service) Close() {
	close(s.stopCh)
	s.wg.Wait()
	s.cleanupGroup.Wait()
}

func (s *Service) Probe(ctx context.Context, req Request) (*Result, error) {
	return s.runProbe(ctx, req)
}

func (s *Service) Submit(req Request) (*Job, error) {
	now := time.Now().UTC()
	job := &Job{
		ID:        newJobID(),
		Status:    JobPending,
		Request:   req,
		CreatedAt: now,
		ExpiresAt: now.Add(s.jobTTL),
	}

	s.mu.Lock()
	s.jobs[job.ID] = job
	s.mu.Unlock()

	select {
	case s.queue <- queuedJob{id: job.ID}:
		return cloneJob(job), nil
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
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return cloneJob(job), true
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
	req = normalizeRequest(req)
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	if err := validatePublicTarget(req.Host); err != nil {
		return nil, err
	}

	switch req.Platform {
	case PlatformBedrock:
		return probeBedrock(ctx, req)
	default:
		return probeJava(ctx, req)
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

func probeJava(ctx context.Context, req Request) (*Result, error) {
	timeout := timeoutFromContext(ctx, 5*time.Second)
	modern, modernErr := status.Modern(ctx, req.Host, uint16(req.Port), options.StatusModern{
		EnableSRV: true,
		Timeout:   timeout,
		Ping:      true,
	})
	if modernErr == nil {
		return buildJavaModernResult(req, modern), nil
	}

	legacy, legacyErr := status.Legacy(ctx, req.Host, uint16(req.Port), options.StatusLegacy{
		EnableSRV: true,
		Timeout:   timeout,
	})
	if legacyErr == nil {
		return buildJavaLegacyResult(req, legacy), nil
	}

	return buildErrorResult(req, classifyProbeError(modernErr, legacyErr)), nil
}

func probeBedrock(ctx context.Context, req Request) (*Result, error) {
	timeout := timeoutFromContext(ctx, 5*time.Second)
	bedrock, err := status.Bedrock(ctx, req.Host, uint16(req.Port), options.StatusBedrock{
		Timeout: timeout,
	})
	if err != nil {
		return buildErrorResult(req, classifyProbeError(err)), nil
	}
	return buildBedrockResult(req, bedrock), nil
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
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return errors.New("private or local network targets are not allowed")
		}
		return nil
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
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
	return &copyJob
}
