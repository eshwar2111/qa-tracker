package store

import (
	"slices"
	"strings"
)

// Summary feeds the dashboard.
func (s *Store) Summary(projectID int64) (*Summary, error) {
	cases, err := s.ListCases(projectID, CaseFilter{})
	if err != nil {
		return nil, err
	}
	areas, err := s.ListAreas(projectID)
	if err != nil {
		return nil, err
	}
	sum := &Summary{ByStatus: map[string]int{}, BySeverity: map[string]int{}, Regressions: []Case{}}
	for _, st := range Statuses {
		sum.ByStatus[st] = 0
	}
	byArea := map[string]*AreaCount{}
	for _, a := range areas {
		ac := &AreaCount{Key: a.Key, Name: a.Name, ByStatus: map[string]int{}}
		byArea[a.Key] = ac
	}
	for _, c := range cases {
		sum.Total++
		sum.ByStatus[c.Status]++
		if valid(RetestStatuses, c.Status) {
			sum.NeedsRetest++
		}
		if valid(OpenStatuses, c.Status) {
			sum.OpenBugs++
			sev := c.Severity
			if sev == "" {
				sev = "unset"
			}
			sum.BySeverity[sev]++
		}
		if c.ReopenCount > 0 && c.Status != StatusPass {
			sum.Regressions = append(sum.Regressions, c)
		}
		if ac := byArea[c.AreaKey]; ac != nil {
			ac.ByStatus[c.Status]++
			ac.Total++
		}
	}
	for _, a := range areas {
		if ac := byArea[a.Key]; ac.Total > 0 {
			sum.Areas = append(sum.Areas, *ac)
		}
	}
	if sum.Areas == nil {
		sum.Areas = []AreaCount{}
	}
	if sum.Recent, err = s.recentEvents(projectID, 30); err != nil {
		return nil, err
	}
	if sum.Ideas, err = s.ideaCounts(projectID); err != nil {
		return nil, err
	}
	return sum, nil
}

// Bugs lists open bugs with everything Claude needs to fix them: steps, the
// latest failure remarks, attachment paths, comments, and the previous fix.
func (s *Store) Bugs(projectID int64, statuses []string, areaKey string) ([]Bug, error) {
	if len(statuses) == 0 {
		statuses = OpenStatuses
	}
	cases, err := s.ListCases(projectID, CaseFilter{Statuses: statuses, AreaKey: areaKey})
	if err != nil {
		return nil, err
	}
	out := []Bug{}
	for _, c := range cases {
		b := Bug{ID: c.ID, Key: c.Key, Area: c.AreaKey, Title: c.Title, Priority: c.Priority, Status: c.Status,
			Severity: c.Severity, ReopenCount: c.ReopenCount, Preconditions: c.Preconditions, Steps: c.Steps, Expected: c.Expected}
		events, err := s.caseEvents(c.ID)
		if err != nil {
			return nil, err
		}
		var reportedID int64
		for i := len(events) - 1; i >= 0; i-- {
			e := events[i]
			if e.Kind == "result" && (e.Data["result"] == ResultFail || e.Data["result"] == ResultBlocked) {
				b.Remarks, _ = e.Data["remarks"].(string)
				b.Build, _ = e.Data["build"].(string)
				b.ReportedAt = e.CreatedAt
				reportedID = e.ID
				break
			}
		}
		for _, e := range events {
			if e.Kind == "comment" && e.ID > reportedID {
				if t, ok := e.Data["text"].(string); ok {
					b.Comments = append(b.Comments, "["+string(e.Actor)+"] "+t)
				}
			}
		}
		b.LastFix = lastFix(events)
		atts, err := s.caseAttachments(c.ID)
		if err != nil {
			return nil, err
		}
		for _, a := range atts {
			b.Attachments = append(b.Attachments, a.Path)
		}
		out = append(out, b)
	}
	// Severity first, then priority, so the worst bugs come first.
	rank := map[string]int{"critical": 0, "major": 1, "minor": 2, "trivial": 3, "": 4}
	slices.SortStableFunc(out, func(a, b Bug) int {
		if rank[a.Severity] != rank[b.Severity] {
			return rank[a.Severity] - rank[b.Severity]
		}
		return strings.Compare(a.Priority, b.Priority)
	})
	return out, nil
}
