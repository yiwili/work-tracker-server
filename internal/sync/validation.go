package sync

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	entityIDRegex   = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
	dateRegex       = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	repositoryRegex = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
)

func ValidateEntityID(id string) error {
	if !entityIDRegex.MatchString(id) {
		return fmt.Errorf("invalid entity_id format: must match ^[A-Za-z0-9_.:-]{1,128}$")
	}
	return nil
}

func ValidateFields(entityType EntityType, fields map[string]any, isCreate bool) error {
	switch entityType {
	case TypeWorkItem:
		return validateWorkItem(fields, isCreate)
	case TypeDailyReport:
		return validateDailyReport(fields, isCreate)
	case TypeSetting:
		return validateSetting(fields, isCreate)
	case TypeProjectMapping:
		return validateProjectMapping(fields, isCreate)
	case TypeGiteeLink:
		return validateGiteeLink(fields, isCreate)
	default:
		return fmt.Errorf("unknown entity type: %s", entityType)
	}
}

func validateWorkItem(f map[string]any, isCreate bool) error {
	allowed := map[string]bool{
		"date": true, "title": true, "details": true, "project": true, "category": true,
		"status": true, "origin": true, "suggested_minutes": true, "confirmed_minutes": true,
		"time_locked": true, "time_source": true, "time_reason": true, "started_at": true,
		"ended_at": true, "deleted": true,
	}

	for k := range f {
		if !allowed[k] {
			return fmt.Errorf("unknown field '%s' for work_item", k)
		}
	}

	if isCreate {
		if _, ok := f["date"]; !ok {
			return fmt.Errorf("missing required field 'date'")
		}
		if _, ok := f["title"]; !ok {
			return fmt.Errorf("missing required field 'title'")
		}
	}

	if v, ok := f["date"]; ok && v != nil {
		s, ok := v.(string)
		if !ok || !dateRegex.MatchString(s) {
			return fmt.Errorf("field 'date' must be YYYY-MM-DD")
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return fmt.Errorf("field 'date' is not a valid calendar date: %w", err)
		}
	}

	if v, ok := f["title"]; ok && v != nil {
		s, ok := v.(string)
		trimmed := strings.TrimSpace(s)
		if !ok || len(trimmed) < 1 || len(s) > 500 {
			return fmt.Errorf("field 'title' must be 1..500 characters and non-empty")
		}
	}

	if v, ok := f["details"]; ok && v != nil {
		s, ok := v.(string)
		if !ok || len(s) > 20000 {
			return fmt.Errorf("field 'details' exceeds 20000 characters")
		}
	}

	if v, ok := f["status"]; ok && v != nil {
		s, ok := v.(string)
		valid := map[string]bool{"requested": true, "attempted": true, "changed": true, "verified": true, "delivered": true}
		if !ok || !valid[s] {
			return fmt.Errorf("invalid status '%v'", v)
		}
	}

	if v, ok := f["confirmed_minutes"]; ok && v != nil {
		n, ok := toInt64(v)
		if !ok || n < 0 || n > 1440 {
			return fmt.Errorf("field 'confirmed_minutes' must be an integer between 0..1440")
		}
	}

	if v, ok := f["suggested_minutes"]; ok && v != nil {
		n, ok := toInt64(v)
		if !ok || n < 0 || n > 1440 {
			return fmt.Errorf("field 'suggested_minutes' must be an integer between 0..1440 or null")
		}
	}

	if v, ok := f["time_locked"]; ok && v != nil {
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("field 'time_locked' must be a boolean")
		}
	}

	if v, ok := f["deleted"]; ok && v != nil {
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("field 'deleted' must be a boolean")
		}
	}

	return nil
}

func validateDailyReport(f map[string]any, isCreate bool) error {
	allowed := map[string]bool{
		"target_minutes": true, "confirmed": true, "confirmed_at": true, "deleted": true,
	}

	for k := range f {
		if !allowed[k] {
			return fmt.Errorf("unknown field '%s' for daily_report", k)
		}
	}

	if isCreate {
		if _, ok := f["target_minutes"]; !ok {
			return fmt.Errorf("missing required field 'target_minutes'")
		}
	}

	if v, ok := f["target_minutes"]; ok && v != nil {
		n, ok := toInt64(v)
		if !ok || n < 1 || n > 43200 {
			return fmt.Errorf("field 'target_minutes' must be an integer between 1..43200")
		}
	}

	if v, ok := f["deleted"]; ok && v != nil {
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("field 'deleted' must be a boolean")
		}
	}

	if v, ok := f["confirmed"]; ok && v != nil {
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("field 'confirmed' must be a boolean")
		}
	}

	return nil
}

func validateSetting(f map[string]any, isCreate bool) error {
	allowed := map[string]bool{
		"value": true, "deleted": true,
	}

	for k := range f {
		if !allowed[k] {
			return fmt.Errorf("unknown field '%s' for setting", k)
		}
	}

	if isCreate {
		if _, ok := f["value"]; !ok {
			return fmt.Errorf("missing required field 'value'")
		}
	}

	if v, ok := f["value"]; ok && v != nil {
		_, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("field 'value' must be a JSON object")
		}
		bytes, err := json.Marshal(v)
		if err != nil || len(bytes) > 262144 {
			return fmt.Errorf("field 'value' exceeds maximum size of 256 KiB")
		}
	}

	if v, ok := f["deleted"]; ok && v != nil {
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("field 'deleted' must be a boolean")
		}
	}

	return nil
}

func validateProjectMapping(f map[string]any, isCreate bool) error {
	allowed := map[string]bool{
		"match_mode": true, "pattern": true, "target_type": true, "enterprise_id": true,
		"program_id": true, "issue_type_id": true, "repository": true, "deleted": true,
	}

	for k := range f {
		if !allowed[k] {
			return fmt.Errorf("unknown field '%s' for project_mapping", k)
		}
	}

	if isCreate {
		for _, req := range []string{"match_mode", "pattern", "target_type"} {
			if _, ok := f[req]; !ok {
				return fmt.Errorf("missing required field '%s'", req)
			}
		}
	}

	if v, ok := f["match_mode"]; ok && v != nil {
		s, ok := v.(string)
		if !ok || (s != "prefix" && s != "contains" && s != "exact") {
			return fmt.Errorf("match_mode must be prefix, contains, or exact")
		}
	}

	if v, ok := f["pattern"]; ok && v != nil {
		s, ok := v.(string)
		trimmed := strings.TrimSpace(s)
		if !ok || len(trimmed) < 1 || len(s) > 500 {
			return fmt.Errorf("pattern must be 1..500 characters and non-empty")
		}
	}

	targetType, _ := f["target_type"].(string)
	if targetType == "enterprise" {
		if isCreate {
			if _, ok := f["enterprise_id"]; !ok {
				return fmt.Errorf("missing required field 'enterprise_id' for enterprise target")
			}
			if _, ok := f["program_id"]; !ok {
				return fmt.Errorf("missing required field 'program_id' for enterprise target")
			}
		}
	} else if targetType == "repository" {
		if isCreate {
			if _, ok := f["repository"]; !ok {
				return fmt.Errorf("missing required field 'repository' for repository target")
			}
		}
		if v, ok := f["repository"]; ok && v != nil {
			s, ok := v.(string)
			if !ok || !repositoryRegex.MatchString(s) {
				return fmt.Errorf("repository must be owner/repo format")
			}
		}
	} else if targetType != "" {
		return fmt.Errorf("target_type must be enterprise or repository")
	}

	if v, ok := f["deleted"]; ok && v != nil {
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("field 'deleted' must be a boolean")
		}
	}

	return nil
}

func validateGiteeLink(f map[string]any, isCreate bool) error {
	allowed := map[string]bool{
		"work_item_id": true, "target_scope": true, "remote_id": true,
		"url": true, "published_version": true, "published_at": true, "deleted": true,
	}

	for k := range f {
		if !allowed[k] {
			return fmt.Errorf("unknown field '%s' for gitee_link", k)
		}
	}

	if isCreate {
		if _, ok := f["work_item_id"]; !ok {
			return fmt.Errorf("missing required field 'work_item_id'")
		}
		if _, ok := f["target_scope"]; !ok {
			return fmt.Errorf("missing required field 'target_scope'")
		}
		if _, ok := f["remote_id"]; !ok {
			return fmt.Errorf("missing required field 'remote_id'")
		}
	}

	if v, ok := f["work_item_id"]; ok && v != nil {
		s, ok := v.(string)
		if !ok || len(s) < 1 || len(s) > 128 {
			return fmt.Errorf("field 'work_item_id' must be 1..128 characters")
		}
	}

	if v, ok := f["target_scope"]; ok && v != nil {
		s, ok := v.(string)
		if !ok || len(s) < 1 || len(s) > 200 {
			return fmt.Errorf("field 'target_scope' must be 1..200 characters")
		}
	}

	if v, ok := f["remote_id"]; ok && v != nil {
		s, ok := v.(string)
		if !ok || len(s) < 1 || len(s) > 100 {
			return fmt.Errorf("field 'remote_id' must be 1..100 characters")
		}
	}

	if v, ok := f["deleted"]; ok && v != nil {
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("field 'deleted' must be a boolean")
		}
	}

	return nil
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if float64(int64(n)) != n {
			return 0, false
		}
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

func FillDefaults(entityType EntityType, fields map[string]any) {
	if _, ok := fields["deleted"]; !ok {
		fields["deleted"] = false
	}

	switch entityType {
	case TypeWorkItem:
		if _, ok := fields["project"]; !ok {
			fields["project"] = "未归属项目"
		}
		if _, ok := fields["category"]; !ok {
			fields["category"] = "其他"
		}
		if _, ok := fields["status"]; !ok {
			fields["status"] = "requested"
		}
		if _, ok := fields["origin"]; !ok {
			fields["origin"] = "manual"
		}
		if _, ok := fields["confirmed_minutes"]; !ok {
			fields["confirmed_minutes"] = 0
		}
		if _, ok := fields["time_locked"]; !ok {
			fields["time_locked"] = false
		}
		if _, ok := fields["time_source"]; !ok {
			fields["time_source"] = "manual"
		}
		if _, ok := fields["time_reason"]; !ok {
			fields["time_reason"] = ""
		}
		if _, ok := fields["details"]; !ok {
			fields["details"] = ""
		}
	case TypeDailyReport:
		if _, ok := fields["confirmed"]; !ok {
			fields["confirmed"] = false
		}
	case TypeGiteeLink:
		if _, ok := fields["url"]; !ok {
			fields["url"] = ""
		}
	}
}

func NormalizeDate(t time.Time) string {
	return t.Format(time.RFC3339)
}
