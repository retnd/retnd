package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// The assurance listing is classified twice, here for `retnd assurance list`
// and in the web client for the Dashboard's tile, until the engine owns it.
// testdata/assurance-listing holds the vectors both are asserted against;
// ui/shared/src/test/assurance-listing-vectors.test.ts reads the same files.

type vectorPlacement struct {
	Medium             string `json:"medium"`
	Status             string `json:"status"`
	Access             string `json:"access"`
	ScrubState         string `json:"scrub_state"`
	ScrubLastCheckedAt string `json:"scrub_last_checked_at"`
	ScrubLastFailureAt string `json:"scrub_last_failure_at"`
	VerificationClass  string `json:"verification_class"`
	VerifiedAt         string `json:"verified_at"`
	RecoveryState      string `json:"recovery_state"`
	RecoveryTestedAt   string `json:"recovery_tested_at"`
	RecoveryCheck      string `json:"recovery_check"`
}

type vectorCase struct {
	Name     string `json:"name"`
	Now      string `json:"now"`
	Artifact struct {
		ID          string            `json:"id"`
		Quarantined bool              `json:"quarantined"`
		Placements  []vectorPlacement `json:"placements"`
	} `json:"artifact"`
	Due *struct {
		Overdue bool `json:"overdue"`
		Never   bool `json:"never"`
	} `json:"due"`
	Policy *struct {
		Off   bool   `json:"off"`
		Basis string `json:"basis"`
	} `json:"policy"`
	Want struct {
		Content  string   `json:"content"`
		Recovery string   `json:"recovery"`
		Buckets  []string `json:"buckets"`
		Reason   string   `json:"reason"`
	} `json:"want"`
}

func vectorTime(t *testing.T, s string) time.Time {
	t.Helper()
	if s == "" {
		return time.Time{}
	}
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("vector time %q: %v", s, err)
	}
	return v
}

func TestAssuranceListingVectors(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "assurance-listing", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no vectors found (%v)", err)
	}
	total := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Cases []vectorCase `json:"cases"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, c := range doc.Cases {
			total++
			t.Run(filepath.Base(file)+"/"+c.Name, func(t *testing.T) {
				now := vectorTime(t, c.Now)
				setID := filepath.Dir(c.Artifact.ID)
				a := Artifact{ID: c.Artifact.ID, BackupSetID: setID, Name: filepath.Base(c.Artifact.ID), Quarantined: c.Artifact.Quarantined}
				for _, p := range c.Artifact.Placements {
					medium := p.Medium
					if medium == "" {
						medium = "local"
					}
					a.Placements = append(a.Placements, Placement{
						Medium: medium, Status: p.Status, Access: p.Access,
						ScrubState: p.ScrubState, ScrubLastCheckedAt: vectorTime(t, p.ScrubLastCheckedAt),
						ScrubLastFailureAt: vectorTime(t, p.ScrubLastFailureAt),
						VerificationClass:  p.VerificationClass, VerifiedAt: vectorTime(t, p.VerifiedAt),
						RecoveryState: p.RecoveryState, RecoveryTestedAt: vectorTime(t, p.RecoveryTestedAt),
						RecoveryCheck: p.RecoveryCheck,
					})
				}
				var due []AssuranceDueItem
				if c.Due != nil {
					item := AssuranceDueItem{ArtifactID: a.ID, Overdue: c.Due.Overdue, Due: c.Due.Overdue}
					if !c.Due.Never {
						item.LastAssuredAt = now.Add(-time.Hour)
					}
					due = append(due, item)
				}
				var policies map[string]SetRecoveryPolicy
				if c.Policy != nil {
					policies = map[string]SetRecoveryPolicy{setID: {Off: c.Policy.Off, Basis: PolicyBasis(c.Policy.Basis)}}
				}
				got := ListAssurance([]Artifact{a}, due, policies, now)
				if len(got) != 1 {
					t.Fatalf("want one row, got %+v", got)
				}
				row := got[0]
				if s := row.Content.rendered(); s != c.Want.Content {
					t.Errorf("content = %q, want %q", s, c.Want.Content)
				}
				if s := row.Recovery.rendered(); s != c.Want.Recovery {
					t.Errorf("recovery = %q, want %q", s, c.Want.Recovery)
				}
				buckets := make([]string, len(row.Buckets))
				for i, b := range row.Buckets {
					buckets[i] = string(b)
				}
				want := c.Want.Buckets
				if want == nil {
					want = []string{}
				}
				if !slices.Equal(buckets, want) {
					t.Errorf("buckets = %v, want %v", buckets, want)
				}
				if row.Reason != c.Want.Reason {
					t.Errorf("reason = %q\n                want %q", row.Reason, c.Want.Reason)
				}
			})
		}
	}
	if total < 30 {
		t.Errorf("only %d vectors were read; the files are the contract, so a lost file is a failure", total)
	}
}
