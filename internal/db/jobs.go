package db

// JobKind is the jobs.kind column.
type JobKind string

// Job kinds.
const (
	KindScan    JobKind = "scan"
	KindListing JobKind = "listing"
	KindCancel  JobKind = "cancel"
)

// JobStatus is the jobs.status column.
type JobStatus string

// Job statuses.
const (
	StatusQueued    JobStatus = "queued"
	StatusRunning   JobStatus = "running"
	StatusDone      JobStatus = "done"
	StatusFailed    JobStatus = "failed"
	StatusCancelled JobStatus = "cancelled"
)

// ScanParams is the params JSON of a scan job. Trigger is "schedule" or "manual".
type ScanParams struct {
	Trigger string `json:"trigger"`
}

// ListingParams is the params JSON of a listing job. Mode is "children" or
// "subtree".
type ListingParams struct {
	Mode       string   `json:"mode"`
	Exts       []string `json:"exts,omitempty"`
	MinSize    int64    `json:"min_size,omitempty"`
	MinAgeDays int      `json:"min_age_days,omitempty"`
}

// CancelParams is the params JSON of a cancel job.
type CancelParams struct {
	JobID int64 `json:"job_id"`
}

// Progress is the jobs.progress JSON the scanner writes every 2 s.
type Progress struct {
	Dirs         int64   `json:"dirs"`
	Files        int64   `json:"files"`
	BytesLogical int64   `json:"bytes_logical"`
	BytesAlloc   int64   `json:"bytes_alloc"`
	Errors       int64   `json:"errors"`
	RatePerSec   float64 `json:"rate_per_sec"`
	ETASeconds   *int64  `json:"eta_seconds"`
	// Phase is walk, finalize or publish.
	Phase string `json:"phase"`
}

// ScanStatus is the scans.status column. A scan is running, then done, failed
// or cancelled; a done scan becomes deleted once its superseded generation
// file is removed.
type ScanStatus string

// Scan statuses.
const (
	ScanRunning   ScanStatus = "running"
	ScanDone      ScanStatus = "done"
	ScanFailed    ScanStatus = "failed"
	ScanCancelled ScanStatus = "cancelled"
	ScanDeleted   ScanStatus = "deleted"
)

// ListingProgress is the jobs.progress JSON of a listing job.
type ListingProgress struct {
	Rows          int64 `json:"rows"`
	Truncated     bool  `json:"truncated"`
	Errors        int64 `json:"errors"`
	MountsSkipped int64 `json:"mounts_skipped"`
}

// AvailableMount is one element of scanner_status.available_mounts.
type AvailableMount struct {
	ContainerPath string `json:"container_path"`
	HostPath      string `json:"host_path"`
	FSType        string `json:"fstype"`
	ReadOnly      bool   `json:"read_only"`
}

// IssueKind is the config_issues.kind column.
type IssueKind string

// Issue kinds.
const (
	IssueCrossRootDuplicate IssueKind = "cross_root_duplicate"
	IssueWritableMount      IssueKind = "writable_mount"
	IssueEIO                IssueKind = "eio"
	IssueInvalidSchedule    IssueKind = "invalid_schedule"
)

// NodesIssueDetails is the details JSON of a writable_mount or eio issue:
// up to 10 node references (<scan_id>.<dir_id>) and the total count.
type NodesIssueDetails struct {
	Nodes []string `json:"nodes"`
	Count int64    `json:"count"`
}

// WritableMountDetails is the details JSON of a writable_mount issue.
type WritableMountDetails = NodesIssueDetails

// EIODetails is the details JSON of an eio issue.
type EIODetails = NodesIssueDetails

// CrossRootDuplicateDetails is the details JSON of a cross_root_duplicate
// issue: the assigned path whose directory another root also scanned.
type CrossRootDuplicateDetails struct {
	UserPathID  int64  `json:"user_path_id"`
	OtherRootID int64  `json:"other_root_id"`
	Node        string `json:"node"`
}

// InvalidScheduleDetails is the details JSON of an invalid_schedule issue.
type InvalidScheduleDetails struct {
	Schedule string `json:"schedule"`
	Error    string `json:"error"`
}
