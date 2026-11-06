package history

import "time"

type HistoryWorkItem struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Details    string     `json:"details"`
	Project    string     `json:"project"`
	WorkDate   *string    `json:"work_date"`
	Source     string     `json:"source"`
	SourceID   string     `json:"source_id"`
	URL        string     `json:"url"`
	ImportedAt time.Time  `json:"imported_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type ImportItemInput struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Details    string  `json:"details"`
	Project    string  `json:"project"`
	WorkDate   *string `json:"work_date"`
	Source     string  `json:"source"`
	SourceID   string  `json:"source_id"`
	URL        string  `json:"url"`
	ImportedAt *string `json:"imported_at"`
}

type ImportHistoryInput struct {
	BatchIndex   int               `json:"batch_index"`
	TotalBatches int               `json:"total_batches"`
	Items        []ImportItemInput `json:"items"`
}

type ImportBatchResult struct {
	BatchIndex int `json:"batch_index"`
	Received   int `json:"received"`
	Inserted   int `json:"inserted"`
	Updated    int `json:"updated"`
	Unchanged  int `json:"unchanged"`
	Failed     int `json:"failed"`
}

type ListFilter struct {
	Page     int     `form:"page,default=1"`
	Limit    int     `form:"limit,default=20"`
	DateFrom string  `form:"date_from"`
	DateTo   string  `form:"date_to"`
	HasDate  *bool   `form:"has_date"`
	Project  string  `form:"project"`
	Source   string  `form:"source"`
	Q        string  `form:"q"`
}

type ListResult struct {
	Total int64             `json:"total"`
	Page  int               `json:"page"`
	Limit int               `json:"limit"`
	Items []HistoryWorkItem `json:"items"`
}

type SummaryResult struct {
	TotalRecords     int64            `json:"total_records"`
	EarliestDate     *string          `json:"earliest_date"`
	LatestDate       *string          `json:"latest_date"`
	MissingDateCount int64            `json:"missing_date_count"`
	Sources          map[string]int64 `json:"sources"`
}
