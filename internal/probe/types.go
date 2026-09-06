package probe

import "time"

type Platform string

const (
	PlatformJava    Platform = "java"
	PlatformBedrock Platform = "bedrock"
)

type State string

const (
	StateOnline  State = "online"
	StateOffline State = "offline"
	StateTimeout State = "timeout"
	StateError   State = "error"
)

type Request struct {
	Platform  Platform `json:"platform"`
	Host      string   `json:"host"`
	Port      int      `json:"port"`
	TimeoutMS int      `json:"timeoutMs,omitempty"`
}

type Result struct {
	Platform     Platform  `json:"platform"`
	State        State     `json:"state"`
	CheckedAt    time.Time `json:"checkedAt"`
	IsOnline     bool      `json:"isOnline"`
	Latency      *int64    `json:"latency"`
	OnlineCount  *int64    `json:"onlineCount"`
	MaxPlayers   *int64    `json:"maxPlayers"`
	Version      *string   `json:"version"`
	Protocol     *int64    `json:"protocol"`
	Motd         *string   `json:"motd"`
	Favicon      *string   `json:"favicon"`
	GameMode     *string   `json:"gameMode"`
	MapName      *string   `json:"mapName"`
	SamplePlayer []string  `json:"samplePlayers"`
	Source       string    `json:"source"`
	Error        *string   `json:"error"`
	ResolvedHost *string   `json:"resolvedHost"`
	ResolvedPort *int      `json:"resolvedPort"`
}

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
)

type Job struct {
	ID        string     `json:"id"`
	Status    JobStatus  `json:"status"`
	Request   Request    `json:"request"`
	Result    *Result    `json:"result,omitempty"`
	Error     *string    `json:"error,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	ExpiresAt time.Time  `json:"expiresAt"`
}
