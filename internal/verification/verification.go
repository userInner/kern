// Package verification defines deterministic check evidence and aggregation.
package verification

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidReport = errors.New("verification: invalid report")

// Status is either a per-check result or the final aggregate conclusion.
type Status string

const (
	StatusUnknown        Status = ""
	StatusPassed         Status = "passed"
	StatusFailed         Status = "failed"
	StatusPartial        Status = "partial"
	StatusManualRequired Status = "manual_required"
	StatusNotRun         Status = "not_run"
	StatusVerified       Status = "verified"
)

// Evidence points to a durable fact rather than a model assertion.
type Evidence struct {
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Summary string `json:"summary"`
	Digest  string `json:"digest,omitempty"`
}

// CheckResult is one independently auditable verifier result.
type CheckResult struct {
	Verifier string     `json:"verifier"`
	Status   Status     `json:"status"`
	Required bool       `json:"required"`
	Summary  string     `json:"summary"`
	Evidence []Evidence `json:"evidence"`
}

// Report is the aggregate verification conclusion for one Attempt.
type Report struct {
	Status    Status        `json:"status"`
	Checks    []CheckResult `json:"checks"`
	CreatedAt time.Time     `json:"created_at"`
}

// Validate ensures every conclusion has an identity, summary, and durable
// evidence reference before the Engine is allowed to persist it.
func Validate(report Report) error {
	if len(report.Checks) == 0 {
		return fmt.Errorf("%w: no checks", ErrInvalidReport)
	}
	for index, check := range report.Checks {
		if strings.TrimSpace(check.Verifier) == "" || strings.TrimSpace(check.Summary) == "" {
			return fmt.Errorf("%w: check %d has no identity or summary", ErrInvalidReport, index)
		}
		switch check.Status {
		case StatusPassed, StatusFailed, StatusPartial, StatusManualRequired, StatusNotRun:
		default:
			return fmt.Errorf("%w: check %s has status %q", ErrInvalidReport, check.Verifier, check.Status)
		}
		if len(check.Evidence) == 0 {
			return fmt.Errorf("%w: check %s has no evidence", ErrInvalidReport, check.Verifier)
		}
		for _, evidence := range check.Evidence {
			if strings.TrimSpace(evidence.Kind) == "" || strings.TrimSpace(evidence.Ref) == "" ||
				strings.TrimSpace(evidence.Summary) == "" {
				return fmt.Errorf("%w: check %s has incomplete evidence", ErrInvalidReport, check.Verifier)
			}
		}
	}
	return nil
}

// Aggregate computes the documented precedence without hiding individual
// checks: required failure, manual verification, partial coverage, verified.
func Aggregate(checks []CheckResult) Status {
	if len(checks) == 0 {
		return StatusNotRun
	}
	manual := false
	partial := false
	for _, check := range checks {
		if check.Required && check.Status == StatusFailed {
			return StatusFailed
		}
		switch check.Status {
		case StatusManualRequired:
			manual = true
		case StatusFailed, StatusPartial:
			partial = true
		case StatusNotRun:
			// An irrelevant optional verifier is intentionally omitted from
			// coverage. A required verifier that could not run still makes the
			// final result partial instead of silently verified.
			partial = partial || check.Required
		}
	}
	if manual {
		return StatusManualRequired
	}
	if partial {
		return StatusPartial
	}
	return StatusVerified
}
