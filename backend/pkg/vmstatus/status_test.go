package vmstatus

import (
	"reflect"
	"testing"
	"time"

	etypes "github.com/63747756/jingjiaagent/backend/ent/types"
	"github.com/63747756/jingjiaagent/backend/pkg/taskflow"
)

func TestInputDoesNotExposeReportedStatus(t *testing.T) {
	if _, ok := reflect.TypeFor[Input]().FieldByName("ReportedStatus"); ok {
		t.Fatal("Input should not expose ReportedStatus")
	}
}

func TestLiveRuntimeObservationPreservesSleepAndOfflineSemantics(t *testing.T) {
	now := time.Now()
	stale := []*etypes.Condition{{Type: etypes.ConditionTypeHibernated}}
	for _, status := range []taskflow.VirtualMachineStatus{taskflow.VirtualMachineStatusHibernated, taskflow.VirtualMachineStatusOffline, taskflow.VirtualMachineStatusOnline, taskflow.VirtualMachineStatusPending} {
		if got := Resolve(Input{RuntimeStatus: status, Conditions: stale, CreatedAt: now, Now: now}); got != status {
			t.Fatalf("live status %s overridden by stale business state: %s", status, got)
		}
	}
	if got := Resolve(Input{RuntimeStatus: taskflow.VirtualMachineStatusHibernated, IsRecycled: true}); got != taskflow.VirtualMachineStatusOffline {
		t.Fatal("recycled environment became resumable")
	}
	if got := Resolve(Input{RuntimeStatus: "invented", CreatedAt: now, Now: now}); got != taskflow.VirtualMachineStatusPending {
		t.Fatal("unrecognized runtime status bypassed original fallback")
	}
}

func TestResolve(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		input Input
		want  taskflow.VirtualMachineStatus
	}{
		{
			name: "is recycled overrides everything",
			input: Input{
				Online:     true,
				IsRecycled: true,
				CreatedAt:  now.Add(-10 * time.Minute),
				Now:        now,
			},
			want: taskflow.VirtualMachineStatusOffline,
		},
		{
			name: "online returns online before conditions",
			input: Input{
				Online: true,
				Conditions: []*etypes.Condition{
					{Type: etypes.ConditionTypeFailed},
				},
				CreatedAt: now.Add(-10 * time.Minute),
				Now:       now,
			},
			want: taskflow.VirtualMachineStatusOnline,
		},
		{
			name: "online returns online",
			input: Input{
				Online:    true,
				CreatedAt: now.Add(-10 * time.Minute),
				Now:       now,
			},
			want: taskflow.VirtualMachineStatusOnline,
		},
		{
			name: "failed condition returns offline",
			input: Input{
				Conditions: []*etypes.Condition{
					{Type: etypes.ConditionTypeFailed},
				},
				CreatedAt: now.Add(-10 * time.Minute),
				Now:       now,
			},
			want: taskflow.VirtualMachineStatusOffline,
		},
		{
			name: "hibernated condition returns hibernated",
			input: Input{
				Conditions: []*etypes.Condition{
					{Type: etypes.ConditionTypeHibernated},
				},
				CreatedAt: now.Add(-10 * time.Minute),
				Now:       now,
			},
			want: taskflow.VirtualMachineStatusHibernated,
		},
		{
			name: "ready older than three minutes returns offline",
			input: Input{
				Conditions: []*etypes.Condition{
					{Type: etypes.ConditionTypeReady},
				},
				CreatedAt: now.Add(-3*time.Minute - time.Second),
				Now:       now,
			},
			want: taskflow.VirtualMachineStatusOffline,
		},
		{
			name: "ready within three minutes stays pending",
			input: Input{
				Conditions: []*etypes.Condition{
					{Type: etypes.ConditionTypeReady},
				},
				CreatedAt: now.Add(-2 * time.Minute),
				Now:       now,
			},
			want: taskflow.VirtualMachineStatusPending,
		},
		{
			name: "defaults to pending",
			input: Input{
				CreatedAt: now,
				Now:       now,
			},
			want: taskflow.VirtualMachineStatusPending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.input)
			if got != tt.want {
				t.Fatalf("Resolve() = %q, want %q", got, tt.want)
			}
		})
	}
}
