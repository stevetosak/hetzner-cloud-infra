package terraform

import (
	"os"
	"path/filepath"
	"testing"
)

const fixtureTfvars = `workers = {
  k8swk1 = {
    private_ip  = "10.0.2.6"
    server_type = "cx23"
    labels = {
      role = "worker"
    }
  }
  k8swk2 = {
    private_ip  = "10.0.2.7"
    server_type = "cx23"
    labels = {
      role = "worker"
    }
  }
  k8swk3 = {
    private_ip  = "10.0.2.8"
    server_type = "cx23"
    labels = {
      role = "worker"
    }
  }
}
`

func writeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "terraform.tfvars")
	if err := os.WriteFile(path, []byte(fixtureTfvars), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

func TestReadWorkers(t *testing.T) {
	path := writeFixture(t)

	workers, err := ReadWorkers(path)
	if err != nil {
		t.Fatalf("ReadWorkers() error: %v", err)
	}

	if len(workers) != 3 {
		t.Fatalf("ReadWorkers() returned %d workers, want 3: %+v", len(workers), workers)
	}

	w1, ok := workers["k8swk1"]
	if !ok {
		t.Fatal("k8swk1 not found")
	}
	if w1.PrivateIP != "10.0.2.6" {
		t.Errorf("k8swk1.PrivateIP = %q, want 10.0.2.6", w1.PrivateIP)
	}
	if w1.ServerType != "cx23" {
		t.Errorf("k8swk1.ServerType = %q, want cx23", w1.ServerType)
	}
	if w1.Labels["role"] != "worker" {
		t.Errorf("k8swk1.Labels[role] = %q, want worker", w1.Labels["role"])
	}
}

func TestAddWorker(t *testing.T) {
	path := writeFixture(t)

	err := AddWorker(path, "k8swk4", Worker{
		PrivateIP:  "10.0.2.9",
		ServerType: "cx23",
		Labels:     map[string]string{"role": "worker"},
	})
	if err != nil {
		t.Fatalf("AddWorker() error: %v", err)
	}

	workers, err := ReadWorkers(path)
	if err != nil {
		t.Fatalf("ReadWorkers() after add error: %v", err)
	}
	if len(workers) != 4 {
		t.Fatalf("expected 4 workers after add, got %d: %+v", len(workers), workers)
	}
	w4, ok := workers["k8swk4"]
	if !ok {
		t.Fatal("k8swk4 not found after AddWorker")
	}
	if w4.PrivateIP != "10.0.2.9" {
		t.Errorf("k8swk4.PrivateIP = %q, want 10.0.2.9", w4.PrivateIP)
	}

	// Existing workers must survive the round-trip untouched.
	if workers["k8swk1"].PrivateIP != "10.0.2.6" {
		t.Errorf("k8swk1 was mutated by AddWorker: %+v", workers["k8swk1"])
	}
}

func TestAddWorker_AlreadyExists(t *testing.T) {
	path := writeFixture(t)

	err := AddWorker(path, "k8swk1", Worker{PrivateIP: "10.0.2.99", ServerType: "cx23"})
	if err == nil {
		t.Fatal("AddWorker() expected error for duplicate name, got nil")
	}
}

func TestRemoveWorker(t *testing.T) {
	path := writeFixture(t)

	if err := RemoveWorker(path, "k8swk2"); err != nil {
		t.Fatalf("RemoveWorker() error: %v", err)
	}

	workers, err := ReadWorkers(path)
	if err != nil {
		t.Fatalf("ReadWorkers() after remove error: %v", err)
	}
	if len(workers) != 2 {
		t.Fatalf("expected 2 workers after remove, got %d: %+v", len(workers), workers)
	}
	if _, ok := workers["k8swk2"]; ok {
		t.Error("k8swk2 still present after RemoveWorker")
	}
	if _, ok := workers["k8swk1"]; !ok {
		t.Error("k8swk1 was removed unexpectedly")
	}
	if _, ok := workers["k8swk3"]; !ok {
		t.Error("k8swk3 was removed unexpectedly")
	}
}

func TestRemoveWorker_NotFound(t *testing.T) {
	path := writeFixture(t)

	err := RemoveWorker(path, "k8swk-does-not-exist")
	if err == nil {
		t.Fatal("RemoveWorker() expected error for missing name, got nil")
	}
}

func TestRemoveWorker_LastOneLeavesValidHCL(t *testing.T) {
	path := writeFixture(t)

	for _, name := range []string{"k8swk1", "k8swk2", "k8swk3"} {
		if err := RemoveWorker(path, name); err != nil {
			t.Fatalf("RemoveWorker(%s) error: %v", name, err)
		}
	}

	workers, err := ReadWorkers(path)
	if err != nil {
		t.Fatalf("ReadWorkers() on empty workers map error: %v", err)
	}
	if len(workers) != 0 {
		t.Fatalf("expected 0 workers, got %d", len(workers))
	}
}
