package store

import (
	"strings"
	"testing"
)

// TestVercelDeployPersistence verifies the vercel_deploys table round-trips:
// create as BUILDING, update to READY with API details, latest per
// project+user, and per-user isolation.
func TestVercelDeployPersistence(t *testing.T) {
	s, err := Open(t.TempDir() + "/db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateVercelDeploy("d1", "p1", "u1", "production"); err != nil {
		t.Fatal(err)
	}
	row, err := s.LatestVercelDeploy("p1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.State != VercelDeployBuilding || row.Target != "production" {
		t.Fatalf("row = %+v, want BUILDING production", row)
	}
	if row.CreatedAt == 0 || row.UpdatedAt == 0 {
		t.Fatalf("timestamps missing: %+v", row)
	}

	if err := s.UpdateVercelDeploy("d1", VercelDeployReady, "", "dep-1", "https://demo.vercel.app"); err != nil {
		t.Fatal(err)
	}
	row, _ = s.LatestVercelDeploy("p1", "u1")
	if row.State != VercelDeployReady || row.DeploymentID != "dep-1" || row.URL != "https://demo.vercel.app" {
		t.Fatalf("updated row = %+v", row)
	}

	// A second, newer deploy wins LatestVercelDeploy.
	if err := s.CreateVercelDeploy("d2", "p1", "u1", ""); err != nil {
		t.Fatal(err)
	}
	row, _ = s.LatestVercelDeploy("p1", "u1")
	if row.ID != "d2" {
		t.Fatalf("latest = %s, want d2", row.ID)
	}

	// Other users/projects are isolated.
	other, err := s.LatestVercelDeploy("p1", "u2")
	if err != nil || other != nil {
		t.Fatalf("other user: row=%+v err=%v, want nil", other, err)
	}
}

// TestFailInterruptedVercelDeploys verifies the startup sweep marks only
// still-BUILDING rows as ERROR and leaves terminal rows alone.
func TestFailInterruptedVercelDeploys(t *testing.T) {
	s, err := Open(t.TempDir() + "/db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateVercelDeploy("a", "p1", "u1", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateVercelDeploy("b", "p1", "u1", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateVercelDeploy("a", VercelDeployReady, "", "dep", "url"); err != nil {
		t.Fatal(err)
	}
	if err := s.FailInterruptedVercelDeploys("server restarted"); err != nil {
		t.Fatal(err)
	}
	rows := allVercelDeployRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	states := map[string]string{}
	errors := map[string]string{}
	for _, r := range rows {
		states[r.ID] = r.State
		errors[r.ID] = r.Error
	}
	if states["b"] != VercelDeployError || !strings.Contains(errors["b"], "restarted") {
		t.Fatalf("in-flight row should be ERROR: states=%v errors=%v", states, errors)
	}
	if states["a"] != VercelDeployReady || errors["a"] != "" {
		t.Fatalf("terminal row was swept: states=%v errors=%v", states, errors)
	}
}

func allVercelDeployRows(t *testing.T, s *Store) []VercelDeploy {
	t.Helper()
	rows, err := s.db.Query(`SELECT id, project_id, user_id, state, COALESCE(error,''), COALESCE(deployment_id,''), COALESCE(url,''), COALESCE(target,''), created_at, updated_at FROM vercel_deploys`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []VercelDeploy
	for rows.Next() {
		var d VercelDeploy
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.UserID, &d.State, &d.Error, &d.DeploymentID, &d.URL, &d.Target, &d.CreatedAt, &d.UpdatedAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}
