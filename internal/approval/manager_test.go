package approval

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestApprovalBindsDecisionToRequestID(t *testing.T) {
	manager := NewManager(2, time.Second)
	result := make(chan error, 1)
	go func() {
		result <- manager.Ask(context.Background(), Request{
			ToolName: "exec_shell", Arguments: `{"command":"echo hello"}`, Workspace: `C:\\workspace`,
		})
	}()

	request := <-manager.Requests()
	if request.ID == "" || request.ToolName != "exec_shell" {
		t.Fatalf("unexpected approval request: %+v", request)
	}
	if err := manager.Resolve("unknown-request", DecisionApproved); !errors.Is(err, ErrMissing) {
		t.Fatalf("unknown request should be rejected, got %v", err)
	}
	if err := manager.Resolve(request.ID, DecisionApproved); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("approved request returned error: %v", err)
	}
}

func TestApprovalRejectsAndExpires(t *testing.T) {
	t.Run("rejected", func(t *testing.T) {
		manager := NewManager(1, time.Second)
		result := make(chan error, 1)
		go func() { result <- manager.Ask(context.Background(), Request{ToolName: "write_file", Arguments: `{}`}) }()
		request := <-manager.Requests()
		if err := manager.Resolve(request.ID, DecisionRejected); err != nil {
			t.Fatal(err)
		}
		if err := <-result; !errors.Is(err, ErrRejected) {
			t.Fatalf("expected ErrRejected, got %v", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		manager := NewManager(1, 20*time.Millisecond)
		result := make(chan error, 1)
		go func() { result <- manager.Ask(context.Background(), Request{ToolName: "write_file", Arguments: `{}`}) }()
		request := <-manager.Requests()
		if request.ID == "" {
			t.Fatal("missing request ID")
		}
		if err := <-result; !errors.Is(err, ErrExpired) {
			t.Fatalf("expected ErrExpired, got %v", err)
		}
		if err := manager.Resolve(request.ID, DecisionApproved); !errors.Is(err, ErrMissing) && !errors.Is(err, ErrExpired) {
			t.Fatalf("expired request must not be approved, got %v", err)
		}
	})
}
